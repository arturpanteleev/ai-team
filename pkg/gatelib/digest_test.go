package gatelib

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// digest_test.go — finding 3: a published id@version is immutable, including
// its digest. The built-in definitions were published before the input-artifact
// typing existed, so `input_artifacts` is not part of the serialized body and
// must never change these golden digests. A task started on an older library
// resumes against the same bytes.

func TestPublishedDigestsAreStable(t *testing.T) {
	golden := map[string]string{
		"required_sections@1": "63869761fe12e44fca93ffd7acd00f7cad722187a57166cec421b44f72ad890c",
		"verdict_marker@1":    "0f3894b3f3185e5fda9611c134867c9e6979c8cf52ab70ae627eae5081d08f70",
		"required_phrases@1":  "625c8616b757a653b8e800eb8e47a1da8ac10e022d6065a3b40ebc1a86cebdeb",
		"required_phrases@2":  "5f832f223190db62f3a5fcf288d778778b40e9ce00dc5688a0699dae3583c82d",
		"pr_exists@1":         "580da8b729f942f3dcd9d9eb1f4850ca45e233744bbfdb0941e6104c24f6e524",
		"agent_review@1":      "234105474dd049e7ea9728c706155e8438df5409d9916d930d17415ddd8b4702",
		"agent_review@2":      "80e36e9d89dd4f23e361358217e21c1470e51e26c10884a0abd15810a7e793f7",
		"human_approval@1":    "a7d1a8c0b9678694a0d32f248cf29597a95afa84a573443241d0c3d7f1b8782a",
		"human_approval@2":    "425a2d8f72f590e032b59a763579a47fb1ccb88b4c590c7a66d918abe9780cd3",
	}
	for ref, want := range golden {
		id, version := splitRef(t, ref)
		def, err := Default().Lookup(id, version)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got := Digest(def); got != want {
			t.Fatalf("%s digest changed: got %s, want %s (published definitions are immutable; the serialized body must not change)", ref, got, want)
		}
	}
}

func TestInputArtifactsDoNotChangeDigest(t *testing.T) {
	def, err := Default().Lookup(GateRequiredSections, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(def.InputArtifacts) == 0 {
		t.Fatal("the definition must still carry its input artifact types for validation")
	}
	before := Digest(def)
	without := def
	without.InputArtifacts = nil
	if Digest(without) != before {
		t.Fatal("input_artifacts must not be part of the serialized definition and its digest")
	}
	if !def.AcceptsArtifact(workflow.ArtifactTypeMD) || def.AcceptsArtifact(workflow.ArtifactTypeLink) {
		t.Fatal("input artifact typing must still work independently of the digest")
	}
}

func splitRef(t *testing.T, ref string) (string, int) {
	t.Helper()
	for i := 0; i < len(ref); i++ {
		if ref[i] == '@' {
			id := ref[:i]
			version := 0
			for _, c := range ref[i+1:] {
				version = version*10 + int(c-'0')
			}
			return id, version
		}
	}
	t.Fatalf("bad ref %q", ref)
	return "", 0
}
