//go:build windows

package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	procCreateJobObjectW          = modKernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject  = modKernel32.NewProc("AssignProcessToJobObject")
	procQueryInformationJobObject = modKernel32.NewProc("QueryInformationJobObject")
)

const (
	jobObjectBasicAccountingInformationClass = 1
	jobObjectExtendedLimitInformationClass   = 9
	jobObjectLimitKillOnJobClose             = 0x00002000
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION
type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// processGuard owns OS resources that must outlive Start and die with Wait/Cancel.
type processGuard interface {
	AfterStart(proc *os.Process)
	// WaitChildren blocks until every process in the tree has exited (or ctx is done).
	// On Windows this covers children that outlive cmd.exe (e.g. python training).
	WaitChildren(ctx context.Context) error
	Close()
}

type windowsJobGuard struct {
	mu        sync.Mutex
	job       windows.Handle
	process   windows.Handle // pins the original PID until Close, even after Wait
	proc      *os.Process
	assigned  bool
	started   bool
	cancelled bool
	cancelErr error
	closed    bool
}

func (g *windowsJobGuard) AfterStart(proc *os.Process) {
	if g == nil || proc == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		// Close/Cancel can race with Start's context watcher before this hook.
		_ = proc.Kill()
		return
	}
	if g.started {
		return
	}
	g.started = true
	g.proc = proc
	// cmd.Wait has not started yet, so os.Process still pins this PID while
	// we acquire our own handle. Keep it through output draining and taskkill.
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(proc.Pid))
	if err == nil {
		g.process = h
		if g.job != 0 {
			r1, _, _ := procAssignProcessToJobObject.Call(uintptr(g.job), uintptr(h))
			g.assigned = r1 != 0
		}
	} else {
		// A restricted host may deny job assignment rights but still let us
		// pin, inspect and terminate our own process for the safe fallback.
		g.process, _ = windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(proc.Pid))
	}
	if g.cancelled {
		g.cancelErr = g.cancelLocked()
	}
}

func (g *windowsJobGuard) Cancel() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return os.ErrProcessDone
	}
	if !g.cancelled {
		g.cancelled = true
		// os/exec may invoke Cancel from inside Start, before AfterStart has
		// captured the handle. The hook must finish this pending cancellation.
		if g.started {
			g.cancelErr = g.cancelLocked()
		}
	}
	return g.cancelErr
}

// cancelLocked never targets a PID unless our retained handle pins its identity.
func (g *windowsJobGuard) cancelLocked() error {
	if g.assigned && g.job != 0 {
		// This job already owns the whole tree. Do not also launch taskkill.
		if err := windows.CloseHandle(g.job); err == nil {
			g.job = 0
			return nil
		}
	}
	if g.process == 0 {
		// No identity-pinning handle: os.Process.Kill uses its own handle and
		// safely reports ErrProcessDone after Wait. Never use the bare PID.
		return g.proc.Kill()
	}
	status, err := windows.WaitForSingleObject(g.process, 0)
	if err != nil {
		return err
	}
	if status != uint32(windows.WAIT_TIMEOUT) {
		// The shell may already be reaped while descendants hold output open.
		return os.ErrProcessDone
	}
	// Hold the lock and original handle until the tracked, bounded fallback
	// exits. Close must not release the PID while taskkill is using it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = exec.CommandContext(ctx, "taskkill", "/PID", fmt.Sprint(g.proc.Pid), "/T", "/F").Run()
	if err == nil {
		return nil
	}
	if killErr := windows.TerminateProcess(g.process, 1); killErr == nil {
		return nil
	}
	if status, _ := windows.WaitForSingleObject(g.process, 0); status == windows.WAIT_OBJECT_0 {
		return os.ErrProcessDone
	}
	return err
}

// WaitChildren polls only a successfully assigned job, holding the lock while
// querying so Cancel/Close cannot release and reuse its handle mid-query.
func (g *windowsJobGuard) WaitChildren(ctx context.Context) error {
	if g == nil {
		return nil
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		g.mu.Lock()
		if g.job == 0 || !g.assigned || g.closed {
			g.mu.Unlock()
			return nil
		}
		active, err := jobActiveProcesses(g.job)
		g.mu.Unlock()
		if err != nil || active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (g *windowsJobGuard) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
	if g.process != 0 {
		_ = windows.CloseHandle(g.process)
		g.process = 0
	}
	g.proc = nil
}

func jobActiveProcesses(job windows.Handle) (uint32, error) {
	var info jobObjectBasicAccountingInformation
	var retLen uint32
	r1, _, err := procQueryInformationJobObject.Call(
		uintptr(job),
		uintptr(jobObjectBasicAccountingInformationClass),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
		uintptr(unsafe.Pointer(&retLen)),
	)
	if r1 == 0 {
		return 0, err
	}
	return info.ActiveProcesses, nil
}

// configureCommandCancellation places children into a kill-on-close job and
// wires Cancel to tear down the whole tree. executil.Run manages output pipes
// independently, keeping normal output collection intact until completion.
func configureCommandCancellation(cmd *exec.Cmd) processGuard {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}

	job, _ := createKillOnCloseJob()
	// Always register AfterStart, even if creating the Job failed: the fallback
	// still needs an identity-pinning process handle and startup cancellation.
	guard := &windowsJobGuard{job: job}
	cmd.Cancel = guard.Cancel
	return guard
}

func createKillOnCloseJob() (windows.Handle, error) {
	r1, _, err := procCreateJobObjectW.Call(0, 0)
	if r1 == 0 {
		return 0, err
	}
	job := windows.Handle(r1)
	var info jobObjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	r1, _, err = procSetInformationJobObject.Call(
		uintptr(job),
		uintptr(jobObjectExtendedLimitInformationClass),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if r1 == 0 {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}
