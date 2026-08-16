# Full LoCoMo candidate result

Date: 2026-08-16. This is development evidence for the unreleased 0.8.2
candidate, not a score for the published 0.8.1 package.

The frozen full run used all 10 official LoCoMo conversations and 1,535
eligible questions. History extraction, final answers, and judging used
GPT-5.4 low in isolated sessions. The LoCoMo source was fixed at commit
`3eb6f2c585f5e1699204e3c3bdf7adc5c28cb376`; the judge prompt SHA-256 was
`8ebac1ef60e9ab5caf99079fdaac038b85472e81491ed35e2d2655f3927c76c2`.

| Participant | Correct answers | Judge score |
|---|---:|---:|
| Mem0 equal-model | 1,231 / 1,535 | 80.20% |
| Pulse 0.8.2 candidate | 1,230 / 1,535 | 80.13% |
| Previous Pulse baseline | 959 / 1,535 | 62.48% |
| Mem0 native | 519 / 1,535 | 33.81% |
| Claude Mem native | 513 / 1,535 | 33.42% |

The candidate returns up to four distinct short capsules within 2,400 bytes.
Median context was 120 estimated tokens, maximum context was 198, and warm
retrieval p95 was 70.8 ms. Pulse beat equal-model Mem0 on temporal questions
but finished one correct answer behind overall. It therefore demonstrated a
large improvement without meeting the predeclared acceptance target of beating
Mem0 and reaching 82%.

Failure analysis attributed 120 of the 305 wrong answers to a useful capsule
appearing in the first 12 candidates but not the final four. Another 86 useful
capsules were outside the first 12, 41 were never created, and 29 answers were
still wrong after relevant context was supplied. A follow-up attempt to choose
a merely more diverse fourth capsule added six sources and lost six on a frozen
200-question check, so it was rejected and is not part of this candidate.

The run created no emotional-memory records and did not use emotion strength,
source, or decay. LoCoMo therefore measures recall of conversational content,
including statements about feelings, but not Pulse's emotional-moment feature.

All 1,535 questions produced one receipt with no adapter, retrieval, answer, or
judge errors. Search questions were absent from the test database before and
after the run. Active Personal data, Team, Cursor, and user configuration were
not used.
