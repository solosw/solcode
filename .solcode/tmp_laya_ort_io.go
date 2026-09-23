package main

import (
	"fmt"
	"os"

	ort "github.com/yalue/onnxruntime_go"
)

func main() {
	dll := os.ExpandEnv(`C:\Users\solosw\.solcode\lib\onnxruntime.dll`)
	model := os.ExpandEnv(`C:\Users\solosw\.solcode\models\laya-onnx\model.onnx`)
	ort.SetSharedLibraryPath(dll)
	if err := ort.InitializeEnvironment(); err != nil {
		fmt.Println("init", err)
		os.Exit(1)
	}
	ins, outs, err := ort.GetInputOutputInfo(model)
	if err != nil {
		fmt.Println("info", err)
		os.Exit(1)
	}
	fmt.Println("inputs:")
	for _, in := range ins {
		fmt.Printf("  %s dtype=%v shape=%v\n", in.Name, in.DataType, in.Dimensions)
	}
	fmt.Println("outputs:")
	for _, out := range outs {
		fmt.Printf("  %s dtype=%v shape=%v\n", out.Name, out.DataType, out.Dimensions)
	}
}
