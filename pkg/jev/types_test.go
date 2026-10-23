package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateQuestions(t *testing.T) {
	cases := []struct {
		name    string
		qs      map[string]Question
		wantErr string
	}{
		{"empty", map[string]Question{}, "at least one"},
		{"bad name", map[string]Question{"has space": {Type: "noul", Instructions: "x"}}, "question name"},
		{"dotted name", map[string]Question{"a.b": {Type: "noul", Instructions: "x"}}, "question name"},
		{"leading digit", map[string]Question{"1st": {Type: "noul", Instructions: "x"}}, "question name"},
		{"bad type", map[string]Question{"q": {Type: "essay", Instructions: "x"}}, "type"},
		{"no instructions", map[string]Question{"q": {Type: "noul"}}, "instructions"},
		{"noul ok", map[string]Question{"q": {Type: "noul", Instructions: "x"}}, ""},
		{"noul bad criteria", map[string]Question{"q": {Type: "noul", Instructions: "x", Criteria: json.RawMessage(`{"maybe":"m"}`)}}, "criteria"},
		{"choice one option", map[string]Question{"q": {Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"A"}`)}}, "2 to 255"},
		{"choice ok", map[string]Question{"q": {Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"A","b":"B"}`)}}, ""},
		{"score one level", map[string]Question{"q": {Type: "score", Instructions: "x", Criteria: json.RawMessage(`["low"]`)}}, "2 to 10"},
		{"score ok", map[string]Question{"q": {Type: "score", Instructions: "x", Criteria: json.RawMessage(`["low","high"]`)}}, ""},
		{"one question too large", map[string]Question{"q": {Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"` + strings.Repeat("d", MaxQuestionBytes) + `","b":"B"}`)}}, "limit is"},
		{"questions too large together", func() map[string]Question {
			qs := map[string]Question{}
			for i := 0; i < 4; i++ {
				qs[fmt.Sprintf("q%d", i)] = Question{Type: "choice", Instructions: "x", Criteria: json.RawMessage(`{"a":"` + strings.Repeat("d", MaxQuestionBytes-64) + `","b":"B"}`)}
			}
			return qs
		}(), "across all questions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateQuestions(tc.qs)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestAnswerValue(t *testing.T) {
	n := 0.87
	if got := (Answer{Type: "noul", Noul: &n}).Value(); got != "0.8700" {
		t.Fatalf("noul value = %q", got)
	}
	if got := (Answer{Type: "choice", Choice: "incomplete"}).Value(); got != "incomplete" {
		t.Fatalf("choice value = %q", got)
	}
	if got := (Answer{Type: "score", Score: json.RawMessage(`1.43`)}).Value(); got != "1.4300" {
		t.Fatalf("score value = %q", got)
	}
	if got := (Answer{Type: "score", Score: json.RawMessage(`2`)}).Value(); got != "2.0000" {
		t.Fatalf("integer score value = %q", got)
	}
	if got := (Answer{Type: "score", Score: json.RawMessage(`null`)}).Value(); got != "" {
		t.Fatalf("null score must be empty, got %q", got)
	}
	if got := (Answer{Type: "noul"}).Value(); got != "" {
		t.Fatalf("missing noul should be empty, got %q", got)
	}
}

func TestValidateResponse(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	qs := map[string]Question{
		"done":    {Type: TypeNoul, Instructions: "x"},
		"verdict": {Type: TypeChoice, Instructions: "x", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
		"sev":     {Type: TypeScore, Instructions: "x", Criteria: json.RawMessage(`["a","b","c"]`)},
	}
	good := func() *Response {
		return &Response{Model: "jev-1.13.0", Answers: map[string]Answer{
			"done":    {Type: TypeNoul, Noul: f(0.9)},
			"verdict": {Type: TypeChoice, Choice: "incomplete", Probabilities: map[string]float64{"complete": 0.2, "incomplete": 0.8}, Confidence: f(0.6)},
			"sev":     {Type: TypeScore, Score: json.RawMessage(`1.43`), Probabilities: map[string]float64{"0": 0, "1": 0.57, "2": 0.43}, Confidence: f(0.35)},
		}}
	}
	if err := ValidateResponse(qs, good()); err != nil {
		t.Fatalf("good response rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*Response)
		want string
	}{
		{"empty answers", func(r *Response) { r.Answers = map[string]Answer{} }, "no answer"},
		{"missing question", func(r *Response) { delete(r.Answers, "sev") }, `"sev"`},
		{"type mismatch", func(r *Response) { a := r.Answers["done"]; a.Type = TypeChoice; r.Answers["done"] = a }, "type"},
		{"noul missing", func(r *Response) { r.Answers["done"] = Answer{Type: TypeNoul} }, "noul"},
		{"noul out of range", func(r *Response) { r.Answers["done"] = Answer{Type: TypeNoul, Noul: f(1.2)} }, "0 to 1"},
		{"choice empty", func(r *Response) { a := r.Answers["verdict"]; a.Choice = ""; r.Answers["verdict"] = a }, "choice"},
		{"choice outside options", func(r *Response) { a := r.Answers["verdict"]; a.Choice = "maybe"; r.Answers["verdict"] = a }, "not one of"},
		{"probability out of range", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities["complete"] = 7
			r.Answers["verdict"] = a
		}, "probabilit"},
		{"score missing", func(r *Response) { r.Answers["sev"] = Answer{Type: TypeScore} }, "score"},
		{"score null", func(r *Response) { a := r.Answers["sev"]; a.Score = json.RawMessage(`null`); r.Answers["sev"] = a }, "numeric"},
		{"score not numeric", func(r *Response) { a := r.Answers["sev"]; a.Score = json.RawMessage(`"b"`); r.Answers["sev"] = a }, "numeric"},
		{"score above top level", func(r *Response) { a := r.Answers["sev"]; a.Score = json.RawMessage(`2.5`); r.Answers["sev"] = a }, "0 to 2"},
		{"score without probabilities", func(r *Response) { a := r.Answers["sev"]; a.Probabilities = nil; r.Answers["sev"] = a }, "no probabilities"},
		{"choice without confidence", func(r *Response) { a := r.Answers["verdict"]; a.Confidence = nil; r.Answers["verdict"] = a }, "no confidence"},
		{"confidence out of range", func(r *Response) { a := r.Answers["verdict"]; a.Confidence = f(-0.1); r.Answers["verdict"] = a }, "confidence"},
		{"probabilities over unrelated keys", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"unrelated": 1}
			r.Answers["verdict"] = a
		}, "expected 2"},
		{"probabilities missing an option", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"incomplete": 1}
			r.Answers["verdict"] = a
		}, "expected 2"},
		{"probabilities with a wrong key", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"incomplete": 0.5, "maybe": 0.5}
			r.Answers["verdict"] = a
		}, "missing key"},
		{"score probabilities with bad level key", func(r *Response) {
			a := r.Answers["sev"]
			a.Probabilities = map[string]float64{"0": 0.2, "1": 0.3, "5": 0.5}
			r.Answers["sev"] = a
		}, "missing key"},
		{"probabilities not normalized", func(r *Response) {
			a := r.Answers["verdict"]
			a.Probabilities = map[string]float64{"complete": 0.6, "incomplete": 0.6}
			r.Answers["verdict"] = a
		}, "sum to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := good()
			tc.mut(r)
			err := ValidateResponse(qs, r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("err must wrap ErrInvalidResponse: %v", err)
			}
		})
	}
	extra := good()
	extra.Answers["unrequested"] = Answer{Type: TypeNoul, Noul: f(0.1)}
	if err := ValidateResponse(qs, extra); err != nil {
		t.Fatalf("extra answers must be tolerated: %v", err)
	}
}

func TestProbabilityMapRejectsNull(t *testing.T) {
	for _, body := range []string{
		`{"complete":null,"incomplete":1}`,
		`null`,
	} {
		var p ProbabilityMap
		err := json.Unmarshal([]byte(body), &p)
		if body == `null` {
			if err != nil {
				t.Fatalf("map null should decode as nil: %v", err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "must not be null") {
			t.Fatalf("Unmarshal(%s) err = %v", body, err)
		}
	}
}
