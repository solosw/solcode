package jevlocal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveArtifactsOpenJevLayout(t *testing.T) {
	dir := t.TempDir()
	writeMinimalOpenJev(t, dir, "q4")

	arts, err := ResolveArtifacts(dir, "q4", "open-jev-test")
	if err != nil {
		t.Fatalf("ResolveArtifacts: %v", err)
	}
	if arts.Family != FamilyOpenJev {
		t.Fatalf("Family = %q", arts.Family)
	}
	if arts.ModelID != "open-jev-test" {
		t.Fatalf("ModelID = %q", arts.ModelID)
	}
	if arts.Config.Temperature != 1.05 {
		t.Fatalf("Temperature = %v", arts.Config.Temperature)
	}
	if filepath.Base(arts.ONNXPath) != "model_q4.onnx" {
		t.Fatalf("ONNXPath = %q", arts.ONNXPath)
	}
	if !ArtifactsPresent(dir, "q4") {
		t.Fatal("ArtifactsPresent = false")
	}
}

func TestResolveArtifactsMissingONNX(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "open_jev_config.json"), `{"temperature":1.05,"max_len":512,"max_state_tokens":256,"markers":["[STATE]","[Q]","[OPT]"]}`)
	mustWrite(t, filepath.Join(dir, "tokenizer.json"), `{}`)
	if ArtifactsPresent(dir, "q4") {
		t.Fatal("ArtifactsPresent should be false without onnx")
	}
	if _, err := ResolveArtifacts(dir, "q4", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestDetectFamilyAndResolveLaya(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "rl_agent_config.json"), `{
		"encoder":"answerdotai/ModernBERT-large","max_len":512,"head_max_len":192,
		"temperature":[1.1,1.2,1.3],"temperature_by_options":{"choice:2":1.5}
	}`)
	mustWrite(t, filepath.Join(dir, "tokenizer.json"), `{}`)
	mustWrite(t, filepath.Join(dir, "model.onnx"), "onnx")

	family, err := DetectFamily(dir)
	if err != nil {
		t.Fatal(err)
	}
	if family != FamilyLaya {
		t.Fatalf("DetectFamily = %q", family)
	}
	arts, err := ResolveArtifacts(dir, "", "laya-test")
	if err != nil {
		t.Fatalf("ResolveArtifacts: %v", err)
	}
	if arts.Family != FamilyLaya {
		t.Fatalf("Family = %q", arts.Family)
	}
	if arts.DType != "fp32" {
		t.Fatalf("DType = %q", arts.DType)
	}
	if arts.LayaConfig.Temperature[0] != 1.1 {
		t.Fatalf("temperature = %v", arts.LayaConfig.Temperature)
	}
	if arts.LayaConfig.TemperatureByOptions["choice:2"] != 1.5 {
		t.Fatalf("temperature_by_options = %v", arts.LayaConfig.TemperatureByOptions)
	}
	if filepath.Base(arts.ONNXPath) != "model.onnx" {
		t.Fatalf("ONNXPath = %q", arts.ONNXPath)
	}
}

func TestDecodeLogitsChoiceNoulScore(t *testing.T) {
	plans := []questionPlan{
		{ID: "c", Type: "choice", Options: []string{"a", "b"}},
		{ID: "n", Type: "noul", Options: []string{"no", "yes"}},
		{ID: "s", Type: "score", Options: []string{"low", "mid", "high"}},
	}
	// Strong b, strong yes, mid-weighted score.
	logits := []float32{0, 5, -2, 4, 0, 3, 1}
	answers, err := DecodeLogits(plans, logits, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if answers["c"].Choice != "b" {
		t.Fatalf("choice = %+v", answers["c"])
	}
	if answers["n"].Noul < 0.9 {
		t.Fatalf("noul = %v", answers["n"].Noul)
	}
	if answers["s"].Score < 0.5 || answers["s"].Score > 1.5 {
		t.Fatalf("score = %v", answers["s"].Score)
	}
}

func writeMinimalOpenJev(t *testing.T, dir, dtype string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "open_jev_config.json"), `{
		"pool":"span","temperature":1.05,"markers":["[STATE]","[Q]","[OPT]"],
		"max_state_tokens":256,"max_len":512,"hidden":1024,"base_model":"microsoft/deberta-v3-large"
	}`)
	mustWrite(t, filepath.Join(dir, "tokenizer.json"), `{}`)
	mustWrite(t, filepath.Join(dir, "spm.model"), "spm")
	onnxDir := filepath.Join(dir, "onnx")
	if err := os.MkdirAll(onnxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(onnxDir, "model_"+dtype+".onnx"), "onnx")
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
