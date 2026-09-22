---
name: debug
description: Diagnose a failure or unexpected behavior by reproducing it, forming a hypothesis, and testing that hypothesis against evidence. Use when something is broken and the cause is not yet known — a failing test, a crash, a wrong result, a hang. Do not use to make feature changes.
allowed-tools: Bash Glob Grep LS View Edit ReadObservation
---

# debug

Find the actual cause. A fix applied to a guessed cause is a coincidence, and
coincidences regress.

## Workflow

1. **Reproduce it.** Get a command that fails reliably and run it. If you cannot
   reproduce, that is the finding — say so and describe what you need.
2. **Read the failure, not the symptom.** Full error text, stack trace, and the
   values involved. The message usually names the thing; read it before doing
   anything else.
3. **Localize by bisection.** Narrow the space: which layer, which function,
   which input. Add temporary logging or run a smaller case. Halving the search
   space beats reading files hopefully.
4. **State one hypothesis and test it.** "X is nil because Y returns early" is
   testable. Change one variable, observe, repeat. Do not change several things
   at once — you will not know which one mattered.
5. **Fix the cause, not the symptom.** Suppressing the error, widening a
   catch, or adding a retry hides the defect. If a workaround is genuinely the
   right call, say why.
6. **Prove the fix.** Re-run the reproduction. Add or update a regression test
   so the same defect cannot return silently.

## Rules

- Do not edit before you understand. A speculative edit destroys the evidence.
- Distinguish what you observed from what you inferred, and say which is which.
- When two explanations fit, the one that predicts something you can check is
  the one to test.
- Report the root cause even if the fix turns out to be elsewhere.

## Output

- The reproduction, as an exact command.
- Root cause, with the `path:line` where it lives.
- The fix, and the evidence it works.
- Whether a regression test now covers it.