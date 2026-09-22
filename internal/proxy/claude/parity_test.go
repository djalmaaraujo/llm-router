// This file proves the recorded corpus round-trips through the same
// decode/marshal pattern the proxy uses, using a local decoder built inside
// the test, not the proxy's own code path. Removing UseNumber from
// internal/proxy/proxy.go will not fail anything here; that production path
// is covered by TestPreservesA20DigitIntegerThroughARewrite in
// internal/proxy/proxy_test.go.

package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every recorded body must survive a decode and re-encode unchanged except for
// key order. This is the guard against UseNumber being forgotten: without it,
// a 20-digit id loses precision when it passes through float64.
func TestRecordedBodiesRoundTripWithoutLoss(t *testing.T) {
	files, err := filepath.Glob("../../../testdata/bodies/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no recorded bodies found: %v", err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&decoded); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		out, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}

		var before, after any
		beforeDec := json.NewDecoder(bytes.NewReader(raw))
		beforeDec.UseNumber()
		if err := beforeDec.Decode(&before); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		afterDec := json.NewDecoder(bytes.NewReader(out))
		afterDec.UseNumber()
		if err := afterDec.Decode(&after); err != nil {
			t.Fatalf("%s: %v", file, err)
		}

		var a, b bytes.Buffer
		json.NewEncoder(&a).Encode(before)
		json.NewEncoder(&b).Encode(after)
		if a.String() != b.String() {
			t.Errorf("%s changed on round trip\n before: %s\n  after: %s", filepath.Base(file), a.String(), b.String())
		}
	}
}

// TestLargeIntegerSurvivesRoundTrip is the assertion the brief warns must
// actually fail without UseNumber: a plain float64 decode does not render
// max_tokens as 1e+06 (encoding/json avoids scientific notation there), but
// it does silently round a 20-digit id to the nearest float64, changing its
// last few digits. Removing dec.UseNumber() below made this test fail with:
//
//	want digit string "12345678901234567890" in output, got .../"external_reference_id":12345678901234567000/...
//
// which is exactly the corruption this test exists to catch.
func TestLargeIntegerSurvivesRoundTrip(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/bodies/large-integers.json")
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}

	const digits = "12345678901234567890"
	if !strings.Contains(string(out), digits) {
		t.Errorf("want digit string %q in output, got %s", digits, out)
	}
}

// TestRecordedBodiesRewriteToTheExpectedModel proves ApplyTier's haiku tier
// sets the model, strips thinking, and otherwise leaves every recorded body
// untouched.
func TestRecordedBodiesRewriteToTheExpectedModel(t *testing.T) {
	const wantModel = "claude-haiku-4-5-20251001"
	files, err := filepath.Glob("../../../testdata/bodies/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no recorded bodies found: %v", err)
	}

	for _, file := range files {
		name := filepath.Base(file)
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		var body map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&body); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		before := make(map[string]any, len(body))
		for k, v := range body {
			before[k] = v
		}

		ApplyTier(body, "haiku", wantModel)

		if body["model"] != wantModel {
			t.Errorf("%s: model = %v, want %v", name, body["model"], wantModel)
		}
		if _, still := body["thinking"]; still {
			t.Errorf("%s: thinking must be stripped for haiku", name)
		}

		for k, v := range before {
			switch k {
			// Rewritten deliberately: model is retargeted; thinking and its
			// dependent context_management edits, plus output_config.effort,
			// are stripped because the haiku tier lacks those capabilities.
			case "model", "thinking", "context_management", "output_config":
				continue
			}
			after, ok := body[k]
			if !ok {
				t.Errorf("%s: field %q was dropped by ApplyTier", name, k)
				continue
			}
			beforeJSON, _ := json.Marshal(v)
			afterJSON, _ := json.Marshal(after)
			if string(beforeJSON) != string(afterJSON) {
				t.Errorf("%s: field %q changed by ApplyTier\n before: %s\n  after: %s", name, k, beforeJSON, afterJSON)
			}
		}
	}
}
