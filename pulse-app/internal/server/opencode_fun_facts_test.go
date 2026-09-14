package server

import "testing"

func TestProjectOpenCodeFunFactCandidatesAreBoundedAndStable(t *testing.T) {
	values := []string{
		"Fact one.", "Fact two.", "Fact three.", "Fact four.",
		"Fact five.", "Fact six.", "Fact seven.",
	}
	first := projectOpenCodeFunFactCandidates(values)
	second := projectOpenCodeFunFactCandidates(values)
	if first.Schema != openCodeFunFactCandidatesSchema || len(first.Candidates) != 6 {
		t.Fatalf("unexpected projection: %#v", first)
	}
	if first.CandidateDigest != second.CandidateDigest || len(first.CandidateDigest) != 64 {
		t.Fatalf("candidate digest is unstable: %q != %q", first.CandidateDigest, second.CandidateDigest)
	}
	for index, candidate := range first.Candidates {
		if len(candidate.ID) != len("fact_")+24 || candidate.Text != values[index] {
			t.Fatalf("candidate %d is invalid: %#v", index, candidate)
		}
	}
}
