package retrieve

import (
	"fmt"
	"testing"
)

func TestRankPromptCapsulesPrefersAnswerableSpecificMemory(t *testing.T) {
	tests := []struct {
		query      string
		generic    string
		specific   string
		specificID int64
	}{
		{"When did Caroline meet friends, family, and mentors?",
			"Caroline has support from friends, family, and mentors.",
			"Caroline met friends, family, and mentors in the week before 2023-06-09.", 2},
		{"How many children does Melanie have?",
			"Melanie loves her children.", "Melanie has at least three children.", 2},
		{"What is Caroline's relationship status?",
			"Caroline's transition changed her relationships.", "Caroline is single.", 2},
		{"What book did Melanie read from Caroline's suggestion?",
			"Caroline recommended a book to Melanie.", `Melanie read Caroline's suggested book "Becoming Nicole".`, 2},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%d", test.specificID), func(t *testing.T) {
			got := rankPromptCapsules(test.query, []promptCapsuleCandidate{
				{id: 1, text: test.generic, cosine: 0.72, lexicalRank: 1},
				{id: test.specificID, text: test.specific, cosine: 0.64, lexicalRank: 2},
			}, 1)
			if len(got) != 1 || got[0] != test.specificID {
				t.Fatalf("ranking=%v, want [%d]", got, test.specificID)
			}
		})
	}
}

func TestRankPromptCapsulesUsesEventTimeForRecentQuestion(t *testing.T) {
	got := rankPromptCapsules("What did Melanie paint recently?", []promptCapsuleCandidate{
		{id: 1, text: "Melanie painted a horse.", cosine: 0.75, lexicalRank: 1, daysAgo: 120},
		{id: 2, text: "Melanie painted a sunset.", cosine: 0.70, lexicalRank: 2, daysAgo: 7},
	}, 1)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("ranking=%v, want newest event [2]", got)
	}
}

func TestPromptCapsuleAcceptedUsesTheHostSemanticAndLexicalGates(t *testing.T) {
	tests := []struct {
		name      string
		candidate promptCapsuleCandidate
		want      bool
	}{
		{name: "semantic below", candidate: promptCapsuleCandidate{cosine: 0.469}, want: false},
		{name: "semantic accepted", candidate: promptCapsuleCandidate{cosine: 0.47}, want: true},
		{name: "lexical below", candidate: promptCapsuleCandidate{cosine: 0.449, lexicalRank: 3}, want: false},
		{name: "lexical accepted", candidate: promptCapsuleCandidate{cosine: 0.45, lexicalRank: 3}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := promptCapsuleAccepted(test.candidate); got != test.want {
				t.Fatalf("accepted=%v, want %v", got, test.want)
			}
		})
	}
}

func TestPromptCapsuleRequiresTheClaimedEventInsteadOfOnlyThePerson(t *testing.T) {
	tests := []struct {
		query  string
		memory string
		want   bool
	}{
		{
			query:  "How did Mara feel after completing a marathon?",
			memory: "Mara felt relieved after the client moved the launch to next month.",
			want:   false,
		},
		{
			query:  "How does Mara now feel about the launch schedule?",
			memory: "Mara felt relieved after the client moved the launch to next month.",
			want:   true,
		},
		{
			query:  "Was Nora excited about buying a sailboat?",
			memory: "Nora was distressed before her medical scan.",
			want:   false,
		},
		{
			query:  "How did Owen feel about selling his car?",
			memory: "Owen said his anger about a scope change had passed.",
			want:   false,
		},
		{
			query:  "How did Kei react to graduating from law school?",
			memory: "Kei felt lighter after apologizing for forgetting a birthday.",
			want:   false,
		},
		{
			query:  "Did Chen feel guilty about missing a flight?",
			memory: "Chen added a missing lock after finding a race condition.",
			want:   false,
		},
		{
			query:  "Was Alex surprised by receiving a scholarship?",
			memory: "Alex set a private boundary around his mother's treatment.",
			want:   false,
		},
		{
			query:  "How did Kei's feelings change after apologizing to Mira?",
			memory: "Kei felt lighter after he apologized to Mira.",
			want:   true,
		},
		{
			query:  "How does Yuki feel about moving to Kyoto?",
			memory: "Yuki felt anxious about arriving in Kyoto without knowing anyone there.",
			want:   true,
		},
		{
			query:  "Why was today emotionally important for Sam?",
			memory: "Sam cried and laughed after his daughter took her first steps.",
			want:   true,
		},
		{
			query:  "What is Little Women about according to Joanna?",
			memory: "Joanna described Little Women as a story about sisterhood, love, and dreams.",
			want:   true,
		},
		{
			query:  "What causes does John feel passionate about supporting?",
			memory: "John is passionate about improving education and infrastructure.",
			want:   true,
		},
		{
			query:  "Что почувствовала Мара после победы в марафоне?",
			memory: "Мара почувствовала облегчение после переноса запуска.",
			want:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			terms := promptPresupposedEventTerms(test.query)
			if got := promptCapsuleSupportsPresupposedEvent(terms, test.memory); got != test.want {
				t.Fatalf("support=%v terms=%v, want %v", got, terms, test.want)
			}
		})
	}
}

func TestPromptDenseQueryAddsOnlyRecognizedIntent(t *testing.T) {
	location := promptDenseQuery("What state did Nate visit?")
	if location == "What state did Nate visit?" {
		t.Fatal("location query was not expanded")
	}
	namedItem := promptDenseQuery("What book did Joanna recommend?")
	if namedItem == "What book did Joanna recommend?" {
		t.Fatal("named-item query was not expanded")
	}
	ordinary := "What did Nate decide about the project state?"
	if got := promptDenseQuery(ordinary); got != ordinary {
		t.Fatalf("ordinary query changed to %q", got)
	}
}

func TestRankPromptCapsulesPrefersANamedPlaceForALocationQuestion(t *testing.T) {
	got := rankPromptCapsules("What state did Nate visit?", []promptCapsuleCandidate{
		{id: 1, text: "Nate likes helping people.", cosine: 0.74, lexicalRank: 1},
		{id: 2, text: "Nate took his turtles to the beach in Tampa.", cosine: 0.61, lexicalRank: 2},
	}, 1)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("ranking=%v, want named place [2]", got)
	}
}

func TestRankPromptCapsulesKeepsTheConclusionWithItsQuestion(t *testing.T) {
	got := rankPromptCapsules("What did Melanie realize after the charity race?", []promptCapsuleCandidate{
		{id: 1, text: "Melanie ran a charity race for mental health.", cosine: 0.80, lexicalRank: 1},
		{id: 2, text: "Melanie started prioritizing self-care because it helps her care for her family.", cosine: 0.64, lexicalRank: 2},
	}, 1)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("ranking=%v, want conclusion [2]", got)
	}
}

func TestRankPromptCapsulesKeepsTheNamedRecommendation(t *testing.T) {
	got := rankPromptCapsules("What book did Caroline recommend to Melanie?", []promptCapsuleCandidate{
		{id: 1, text: "Melanie said her favorite childhood book was \"Charlotte's Web.\"", cosine: 0.78, lexicalRank: 1},
		{id: 2, text: "Caroline highly recommended \"Becoming Nicole\" by Amy Ellis Nutt.", cosine: 0.65, lexicalRank: 2},
	}, 1)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("ranking=%v, want recommendation [2]", got)
	}
}
