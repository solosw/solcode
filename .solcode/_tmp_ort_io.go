package main

import (
	"fmt"
	"os"
	"path/filepath"

	ort "github.com/yalue/onnxruntime_go"
)

func main() {
	home, _ := os.UserHomeDir()
	lib := filepath.Join(home, ".solcode", "lib", "onnxruntime.dll")
	model := filepath.Join(home, ".solcode", "embeddings", "model_q4f16.onnx")
	if _, err := os.Stat(lib); err != nil {
		fmt.Println("lib missing:", err)
		os.Exit(1)
	}
	ort.SetSharedLibraryPath(lib)
	if err := ort.InitializeEnvironment(); err != nil {
		fmt.Println("init:", err)
		os.Exit(1)
	}
	defer ort.DestroyEnvironment()
	ins, outs, err := ort.GetInputOutputInfo(model)
	if err != nil {
		fmt.Println("info:", err)
		os.Exit(1)
	}
	fmt.Println("INPUTS:")
	for _, in := range ins {
		fmt.Printf("  %s type=%v shape=%v\n", in.Name, in.DataType, in.Dimensions)
	}
	fmt.Println("OUTPUTS:")
	for _, out := range outs {
		fmt.Printf("  %s type=%v shape=%v\n", out.Name, out.DataType, out.Dimensions)
	}
}
