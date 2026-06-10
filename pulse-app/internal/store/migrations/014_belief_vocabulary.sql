-- 014_belief_vocabulary.sql
-- Typed belief vocabulary on events and facts.
--
-- Rationale (2026-04-18, after empathic-memory benchmarking):
--   Retrieval previously treated all events uniformly — same recency decay,
--   no concept of "this is an axiom that never decays" vs "this is a
--   hypothesis that should evaporate quickly".
--
--   The belief-class framework formalizes this distinction with
--   `belief_class` + `confidence_floor` + `archivable`. Events & facts get
--   typed classes that change their decay behavior at retrieval time.
--
-- Classes:
--   axiom         — permanent truths (core wounds, companion identity). No decay.
--   self_model    — companion's own introspective facts. Slow decay, high floor.
--   user_model    — user's psychological profile, wounds, preferences. Slow decay, moderate floor.
--   operational   — day-to-day context, preferences that shift. Normal decay.
--   hypothesis    — provisional reads that need confirmation. Fast decay.
--
-- Decay rates applied at retrieval time (retrieval_v2.py reads this column):
--   axiom       : λ = 0.0     (never decay)
--   self_model  : λ = 0.0005  (half-life ~1400 days)
--   user_model  : λ = 0.001   (half-life ~700 days) — same as current default
--   operational : λ = 0.003   (half-life ~230 days)
--   hypothesis  : λ = 0.005   (half-life ~140 days)
--
-- confidence_floor: minimum retrieval score after decay. A defining-loss axiom
-- with floor=0.85 stays salient even after 10 years. Ordinary events have
-- floor=0.
--
-- archivable: if 0, event is pinned — consolidation pipeline won't mark it for
-- archive/erasure even when stale. Axioms + defining wounds = not archivable.
--
-- provenance: audit trail for how the belief entered state. Values:
--   memory_pattern      — extracted from repeated observations
--   interactive_memory  — from live chat user turns
--   idle_background     — inferred during idle reflection
--   sleep_reflection    — distilled from nightly consolidation
--   manual              — human-authored (seed beliefs, curated memory files)

-- Events: typed belief metadata
ALTER TABLE events ADD COLUMN belief_class TEXT
    CHECK (belief_class IN ('axiom','self_model','user_model','operational','hypothesis'))
    DEFAULT 'operational';
ALTER TABLE events ADD COLUMN confidence_floor REAL NOT NULL DEFAULT 0.0
    CHECK (confidence_floor >= 0.0 AND confidence_floor <= 1.0);
ALTER TABLE events ADD COLUMN archivable INTEGER NOT NULL DEFAULT 1
    CHECK (archivable IN (0, 1));
ALTER TABLE events ADD COLUMN provenance TEXT
    CHECK (provenance IN ('memory_pattern','interactive_memory','idle_background','sleep_reflection','manual'))
    DEFAULT 'interactive_memory';

-- Same on facts (they are atomic beliefs too)
ALTER TABLE facts ADD COLUMN belief_class TEXT
    CHECK (belief_class IN ('axiom','self_model','user_model','operational','hypothesis'))
    DEFAULT 'operational';
ALTER TABLE facts ADD COLUMN confidence_floor REAL NOT NULL DEFAULT 0.0
    CHECK (confidence_floor >= 0.0 AND confidence_floor <= 1.0);
ALTER TABLE facts ADD COLUMN archivable INTEGER NOT NULL DEFAULT 1
    CHECK (archivable IN (0, 1));
ALTER TABLE facts ADD COLUMN provenance TEXT
    CHECK (provenance IN ('memory_pattern','interactive_memory','idle_background','sleep_reflection','manual'))
    DEFAULT 'interactive_memory';

-- Indices: belief_class is the join key when retrieval applies per-class decay
CREATE INDEX IF NOT EXISTS idx_events_belief_class ON events(belief_class);
CREATE INDEX IF NOT EXISTS idx_facts_belief_class ON facts(belief_class);

-- Axiom-preservation invariant: axioms must be non-archivable with floor >= 0.5
-- Enforced at application level (retrieval_v2.py validates on insert for v3+)
-- SQL triggers could enforce but keep it lightweight for now.
