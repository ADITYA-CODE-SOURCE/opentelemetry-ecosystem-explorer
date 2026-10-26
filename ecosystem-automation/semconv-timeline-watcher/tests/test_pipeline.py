# Copyright The OpenTelemetry Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

"""End-to-end pipeline behaviour on fixture repositories: replay, failure, coverage and completion."""

import pytest
from semconv_timeline_watcher import pipeline, revisions
from semconv_timeline_watcher.accepted import TIMELINE_PATH
from semconv_timeline_watcher.gitrepo import GitError
from semconv_timeline_watcher.state import REGISTRY_DIR, Store
from semconv_timeline_watcher.validation import record_problems
from watcher_common.testing import run_git

from .conftest import CORE, GENAI, RELEASES, FakeClient, commit, release, run_core, run_genai, write


def test_unreviewed_releases_become_pending_with_ancestor_baselines(tmp_path, accepted, accepted_file, core):
    store, before = Store(tmp_path / "reg"), accepted_file.read_bytes()
    state, report = run_core(store, accepted, core)
    assert sorted(report["actions"]) == [
        f"{CORE}@v1.0.1",
        f"{CORE}@v1.1.0",
    ]  # v1.0.0 is accepted; drafts and prereleases ignored
    patch, minor = (store.read_record(CORE, f"{CORE}@{t}") for t in ("v1.0.1", "v1.1.0"))
    assert patch["baselineTag"] == minor["baselineTag"] == "v1.0.0"  # the patch is not v1.1.0's baseline
    assert minor["date"] == "2026-02-02" and minor["dateBasis"] == "release-published-at"
    assert minor["compareUrl"].endswith(f"/compare/{core['shas']['v1.0.0']}...{core['shas']['v1.1.0']}")
    assert {r["status"] for r in state["revisions"].values()} == {"pending"}
    assert state["cursor"] is None
    assert accepted_file.read_bytes() == before  # automation never edits accepted history


def test_replaying_unchanged_inputs_changes_nothing(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    store.save_state({"schemaVersion": 1, "sources": {CORE: state}})
    snapshot = {p: p.read_bytes() for p in (tmp_path / "reg").rglob("*.json")}
    again, report = run_core(store, accepted, core, state)
    assert set(report["actions"].values()) == {"unchanged"} and again == state
    assert {p: p.read_bytes() for p in (tmp_path / "reg").rglob("*.json")} == snapshot


def test_a_moved_tag_is_flagged_not_silently_re_extracted(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    original = store.read_record(CORE, f"{CORE}@v1.1.0")
    write(core["path"], "model/extra/registry.yaml", "groups: []\n")
    moved = commit(core["path"], "later", "2026-02-10T00:00:00+00:00", core["monkeypatch"])
    run_git(core["path"], "tag", "-f", "v1.1.0", moved)
    state, report = run_core(store, accepted, core, state)
    assert report["actions"][f"{CORE}@v1.1.0"] == "tag-changed"
    assert state["revisions"][f"{CORE}@v1.1.0"]["tagChanged"] == {"previous": core["shas"]["v1.1.0"], "current": moved}
    assert store.read_record(CORE, f"{CORE}@v1.1.0") == original
    state, report = run_core(store, accepted, core, state, tag="v1.1.0")  # explicit reconciliation
    assert report["actions"][f"{CORE}@v1.1.0"] == "updated"
    assert "tagChanged" not in state["revisions"][f"{CORE}@v1.1.0"]
    assert store.read_record(CORE, f"{CORE}@v1.1.0")["commit"] == moved


def test_an_accepted_release_whose_tag_moved_is_reported_for_reconciliation(tmp_path, accepted, core):
    run_git(core["path"], "tag", "-f", "v1.0.0", core["shas"]["v1.0.1"])
    state, _ = run_core(Store(tmp_path / "reg"), accepted, core)
    assert state["reconcile"] == [
        {"key": f"{CORE}@v1.0.0", "expected": core["shas"]["v1.0.0"], "actual": core["shas"]["v1.0.1"]}
    ]


def test_one_unresolvable_release_fails_alone(tmp_path, accepted, core):
    client = FakeClient(
        [*RELEASES, release("v1.5.0", "2026-04-01T00:00:00Z")]
    )  # published upstream, tag not in the clone
    state, report = pipeline.process_core(
        Store(tmp_path / "reg"), accepted, core["git"], client, pipeline.empty_source_state(), {}
    )
    assert report["actions"][f"{CORE}@v1.5.0"] == "failed"
    assert state["revisions"][f"{CORE}@v1.5.0"]["status"] == "failed"
    assert state["revisions"][f"{CORE}@v1.1.0"]["status"] == "pending"


def test_pending_work_blocks_the_cursor_and_completion_needs_explicit_dispositions(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    minor_key, patch_key = f"{CORE}@v1.1.0", f"{CORE}@v1.0.1"
    minor = store.read_record(CORE, minor_key)
    for candidate in minor["candidates"]:
        candidate["disposition"] = {"status": "excluded", "reason": "scoped change; not a timeline event"}
    store.write_record(minor)
    state = pipeline.refresh_statuses(store, accepted, state)
    assert state["revisions"][minor_key]["status"] == "complete"
    assert state["cursor"] is None  # the earlier v1.0.1 is still pending, so the cursor stays put
    patch = store.read_record(CORE, patch_key)
    patch["noMilestone"] = {"reason": "only a code-generation fix"}
    store.write_record(patch)
    state = pipeline.refresh_statuses(store, accepted, state)
    assert state["revisions"][patch_key]["status"] == "complete" and state["cursor"] == minor_key


def test_maintainer_dispositions_survive_a_forced_replay(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    key = f"{CORE}@v1.1.0"
    record = store.read_record(CORE, key)
    record["candidates"][0]["disposition"] = {"status": "accepted", "eventId": "k8s-existing"}
    store.write_record(record)
    run_core(store, accepted, core, state, force=True)
    assert store.read_record(CORE, key)["candidates"][0]["disposition"] == {
        "status": "accepted",
        "eventId": "k8s-existing",
    }


def test_an_interrupted_run_is_recoverable(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    store.save_state({"schemaVersion": 1, "sources": {CORE: state}})
    store.record_path(CORE, f"{CORE}@v1.1.0").unlink()  # state names a record whose write never finished
    (tmp_path / "reg" / "candidates" / "v1.1.0.json.tmp").write_text("{")
    assert store.recover() == 1
    broken = pipeline.refresh_statuses(store, accepted, store.load_state()["sources"][CORE])
    assert broken["revisions"][f"{CORE}@v1.1.0"]["status"] == "failed"
    fixed, report = run_core(store, accepted, core, broken)
    assert report["actions"][f"{CORE}@v1.1.0"] == "created"
    assert fixed["revisions"][f"{CORE}@v1.1.0"]["status"] == "pending"


def test_genai_is_commit_based_with_honest_dates_and_no_invented_tags(tmp_path, accepted, genai):
    store = Store(tmp_path / "reg")
    state, report = run_genai(store, accepted, genai)
    start = genai["shas"]["start"]
    (key,) = state["revisions"]
    assert key == f"{GENAI}@{start[:12]}.."  # a stable, open-ended key anchored at the reviewed start
    record = store.read_record(GENAI, key)
    assert "tag" not in record and record["dateBasis"] == "commit-date" and record["date"] == "2026-06-23"
    assert record["baselineCommit"] == start and record["commit"] == genai["shas"]["relevant"]
    assert [c["commit"] for c in record["relevantCommits"]] == [genai["shas"]["relevant"]]  # reference/ is not relevant
    assert record["changelogFragments"] == ["Add `gen_ai.prompt.version` attribute."]
    assert record["candidates"] == [] and record["method"]["structure"]["subjectsAddedToExistingNamespaces"] == 1
    # Definitions already present at the reviewed start are carried over, not proposed as new origins.
    assert not [c for c in record["candidates"] if c["kind"] == "domain"]


def test_a_pending_genai_range_grows_in_place_but_a_reviewed_one_is_never_extended(tmp_path, accepted, genai):
    store = Store(tmp_path / "reg")
    state, _ = run_genai(store, accepted, genai)
    (key,) = state["revisions"]
    write(genai["path"], "docs/gen-ai/new.md", "n\n")
    newer = commit(genai["path"], "docs", "2026-07-01T00:00:00+00:00", genai["monkeypatch"])
    grown, report = run_genai(store, accepted, genai, state)
    assert list(grown["revisions"]) == [key] and report["actions"][key] == "updated"  # unreviewed: one coalesced update
    assert store.read_record(GENAI, key)["commit"] == newer

    # A maintainer records noMilestone for that range. The saved state is stale (still pending) when the next
    # run starts; the run must pick the edit up and must not stretch the reviewed range over new commits.
    record = store.read_record(GENAI, key)
    record["noMilestone"] = {"reason": "an attribute addition inside an existing namespace"}
    store.write_record(record)
    write(genai["path"], "model/gen-ai/extra.yaml", "groups: []\n")
    last = commit(genai["path"], "model", "2026-08-01T00:00:00+00:00", genai["monkeypatch"])
    nxt, _ = run_genai(store, accepted, genai, grown)
    assert nxt["revisions"][key]["status"] == "complete" and nxt["cursor"] == key
    assert store.read_record(GENAI, key)["commit"] == newer  # the reviewed range is untouched
    fresh = store.read_record(GENAI, f"{GENAI}@{newer[:12]}..")
    assert fresh["baselineCommit"] == newer and fresh["commit"] == last
    assert fresh["noMilestone"] is None  # the new commits are evaluated on their own


def test_the_start_is_the_later_of_the_completed_run_and_reviewed_through(tmp_path, accepted, genai):
    store = Store(tmp_path / "reg")
    state, _ = run_genai(store, accepted, genai)
    (key,) = state["revisions"]
    record = store.read_record(GENAI, key)
    record["noMilestone"] = {"reason": "reviewed"}
    store.write_record(record)
    write(genai["path"], "model/gen-ai/extra.yaml", "groups: []\n")
    newer = commit(genai["path"], "more model", "2026-08-01T00:00:00+00:00", genai["monkeypatch"])
    # The accepted coverage mark has moved past the completed run (a maintainer accepted newer commits).
    accepted.sources[GENAI]["reviewedThrough"] = {"commit": newer}
    again, report = run_genai(store, accepted, genai, state)
    assert list(report["actions"]) == ["no-relevant-changes"] and list(again["revisions"]) == [key]


def test_a_manual_replay_is_reviewed_but_never_moves_the_cursor(tmp_path, accepted, genai):
    store = Store(tmp_path / "reg")
    state, report = run_genai(store, accepted, genai, from_ref=genai["shas"]["start"], to_ref=genai["shas"]["relevant"])
    (key,) = state["revisions"]
    assert state["revisions"][key]["manual"] is True
    record = store.read_record(GENAI, key)
    record["noMilestone"] = {"reason": "backfill reviewed"}
    store.write_record(record)
    state = pipeline.refresh_statuses(store, accepted, state)
    assert state["revisions"][key]["status"] == "complete" and state["cursor"] is None and state["order"] == []


def test_nothing_relevant_creates_no_review_work(tmp_path, accepted, genai):
    state, _ = run_genai(Store(tmp_path / "reg"), accepted, genai, from_ref=genai["shas"]["relevant"])
    assert state["revisions"] == {}


def test_rewritten_genai_history_is_an_error_not_a_silent_reset(tmp_path, accepted, genai):
    with pytest.raises(GitError, match="rewritten"):
        run_genai(
            Store(tmp_path / "reg"), accepted, genai, from_ref=genai["shas"]["relevant"], to_ref=genai["shas"]["start"]
        )


def test_manual_range_replays_a_closed_interval_in_either_source(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, report = run_core(store, accepted, core, from_ref="v1.0.0", to_ref="v1.1.0")
    (key,) = report["actions"]
    assert key == f"{CORE}@{core['shas']['v1.0.0'][:12]}..{core['shas']['v1.1.0'][:12]}"
    assert store.read_record(CORE, key)["dateBasis"] == "commit-date"
    state, report = run_core(store, accepted, core, state, from_ref="v1.0.0", to_ref="v1.1.0")
    assert report["actions"][key] == "updated"  # explicit replays always re-extract, keeping dispositions
    with pytest.raises(ValueError, match="not a published release"):
        run_core(store, accepted, core, tag="v7.7.7")


def test_a_core_range_records_its_missing_changelog_evidence_and_must_run_forwards(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    _, report = run_core(store, accepted, core, from_ref="v1.0.0", to_ref="v1.1.0")
    (key,) = report["actions"]
    record = store.read_record(CORE, key)
    assert record["incomplete"]  # core has no changelog.d, so a range cannot claim changelog coverage
    assert any("changelog.d" in g["message"] for g in record["gaps"])
    with pytest.raises(GitError, match="not an ancestor"):
        run_core(store, accepted, core, from_ref="v1.1.0", to_ref="v1.0.0")


def test_a_tag_that_briefly_fails_to_resolve_recovers_without_a_false_tag_move(tmp_path, accepted, core):
    store, key = Store(tmp_path / "reg"), f"{CORE}@v1.1.0"
    state, _ = run_core(store, accepted, core)
    run_git(core["path"], "tag", "-d", "v1.1.0")
    state, report = run_core(store, accepted, core, state)
    assert report["actions"][key] == "failed"
    assert state["revisions"][key]["commit"] == core["shas"]["v1.1.0"]  # the known commit is kept
    run_git(core["path"], "tag", "v1.1.0", core["shas"]["v1.1.0"])
    state, report = run_core(store, accepted, core, state)
    assert report["actions"][key] == "updated"
    assert "tagChanged" not in state["revisions"][key] and state["revisions"][key]["status"] == "pending"
    store.save_state({"schemaVersion": 1, "sources": {CORE: state}})  # previously rejected as invalid


def test_published_candidates_stay_out_of_public_data():
    assert "public" not in REGISTRY_DIR.parts and REGISTRY_DIR.parts[0] == "ecosystem-registry"
    assert "public" in TIMELINE_PATH.parts  # accepted history is the only thing published


def test_an_extractor_record_that_fails_validation_fails_the_revision_and_keeps_the_previous_file(
    tmp_path, accepted, core, monkeypatch
):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    key = f"{CORE}@v1.1.0"
    path = store.record_path(CORE, key)
    before = path.read_bytes()
    good = store.read_record(CORE, key)
    bad = {**good, "candidates": [{"id": "x", "disposition": None}], "date": ""}
    real = pipeline.revisions.build_record
    monkeypatch.setattr(
        pipeline.revisions,
        "build_record",
        lambda spec, *a, **k: bad if spec["reviewKey"] == key else real(spec, *a, **k),
    )
    again, report = run_core(store, accepted, core, state, force=True)
    assert report["actions"][key] == "failed"
    assert "date must be a non-empty string" in again["revisions"][key]["error"]
    assert again["revisions"][key]["status"] == "failed"  # visible, never treated as processed
    assert path.read_bytes() == before and not list((tmp_path / "reg").rglob("*.tmp"))
    assert report["actions"][f"{CORE}@v1.0.1"] != "failed"  # the other revision is unaffected


def _range_record(base, end, *, date, disposition=None):
    """A minimal valid commit-range record from commit `base` to commit `end`."""
    key = f"{GENAI}@{base[:12]}..{end[:12]}"
    candidate = {
        "id": f"{key}/stability/gen_ai/stable",
        "kind": "stability",
        "proposedType": "stability",
        "lane": None,
        "transition": "gen_ai: selected subjects",
        "scope": {"changed": 1, "total": 1},
        "fromStates": {"development": 1},
        "subjects": ["gen_ai.a"],
        "evidence": [],
        "reasons": [],
        "warnings": [],
        "disposition": disposition,
    }
    record = {
        "schemaVersion": 1,
        "reviewKey": key,
        "source": GENAI,
        "kind": "range",
        "commit": end,
        "baselineCommit": base,
        "date": date,
        "dateBasis": "commit-date",
        "incomplete": False,
        "method": {"structure": {"groupsAdded": 0, "groupsRemoved": 0, "subjectsAddedToExistingNamespaces": 0}},
        "gaps": [],
        "relevantCommits": [],
        "noMilestone": None,
        "candidates": [candidate],
    }
    return key, record


def _store_with(tmp_path, records):
    store = Store(tmp_path / "reg")
    for record in records:
        store.write_record(record)
    revisions = {r["reviewKey"]: {"kind": r["kind"], "source": r["source"], "commit": r["commit"]} for r in records}
    return store, {**pipeline.empty_source_state(), "revisions": revisions}


def test_chained_ranges_with_the_same_commit_date_keep_their_logical_order_and_the_cursor(tmp_path, accepted):
    a, b, c = "f" * 40, "e" * 40, "a" * 40
    done = {"status": "excluded", "reason": "reviewed"}
    a_to_b, first = _range_record(a, b, date="2026-05-01", disposition=done)
    b_to_c, second = _range_record(b, c, date="2026-05-01")  # same UTC day; its key sorts before the first one
    assert b_to_c < a_to_b  # a plain (date, key) sort would put B->C first and leave the cursor empty
    store, state = _store_with(tmp_path, [first, second])
    refreshed = pipeline.refresh_statuses(store, accepted, state)
    assert refreshed["order"] == [a_to_b, b_to_c]
    assert (
        refreshed["revisions"][a_to_b]["status"] == "complete" and refreshed["revisions"][b_to_c]["status"] == "pending"
    )
    assert refreshed["cursor"] == a_to_b


def test_a_longer_chain_is_ordered_by_baseline_and_end_whatever_the_dates_and_keys(tmp_path, accepted):
    shas = ["d" * 40, "c" * 40, "b" * 40, "a" * 40]
    pairs = [(shas[i], shas[i + 1]) for i in range(3)]
    made = [_range_record(base, end, date="2026-05-01") for base, end in pairs]
    store, state = _store_with(tmp_path, [r for _, r in reversed(made)])
    assert pipeline.refresh_statuses(store, accepted, state)["order"] == [k for k, _ in made]


def test_independent_ranges_and_releases_keep_the_plain_date_then_key_order(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    plain = sorted(state["order"], key=lambda k: (store.read_record(CORE, k)["date"], k))
    assert state["order"] == plain and state["order"] == [f"{CORE}@v1.0.1", f"{CORE}@v1.1.0"]
    # Releases are never linked by baselines: a release whose baselineCommit equals another's commit is not reordered.
    for key in state["order"]:
        record = store.read_record(CORE, key)
        record["baselineCommit"] = store.read_record(CORE, state["order"][-1])["commit"]
        store.write_record(record)
    assert pipeline.refresh_statuses(store, accepted, state)["order"] == plain


PENDING_KEY = f"{CORE}@v1.1.0"


def _release(core, accepted, tag="v1.1.0", baseline=None, baseline_tag=None):
    return revisions.release_spec(
        source=CORE,
        repository=accepted.sources[CORE]["repository"],
        tag=tag,
        commit=core["shas"][tag],
        date="2026-02-02",
        baseline_tag=baseline_tag,
        baseline_commit=baseline,
    )


def _process(store, core, accepted, spec, src):
    paths = accepted.sources[CORE]["paths"]
    return pipeline._process_spec(store, core["git"], accepted, spec, src, paths, False)


def test_a_record_without_an_ancestor_baseline_is_valid_incomplete_and_has_no_structure(tmp_path, accepted, core):
    store, src = Store(tmp_path / "reg"), pipeline.empty_source_state()
    assert _process(store, core, accepted, _release(core, accepted), src) == "created"
    record = store.read_record(CORE, PENDING_KEY)  # read_record validates the file
    assert record["baselineCommit"] is None and record["incomplete"] is True
    assert "structure" not in record["method"]  # nothing was compared, so no misleading empty counters
    assert any("nothing was compared" in g["message"] for g in record["gaps"])
    assert record_problems(record) == []


def test_a_compared_record_keeps_its_structure_and_the_validator_still_requires_it(tmp_path, accepted, core):
    store, src = Store(tmp_path / "reg"), pipeline.empty_source_state()
    spec = _release(core, accepted, baseline=core["shas"]["v1.0.0"], baseline_tag="v1.0.0")
    assert _process(store, core, accepted, spec, src) == "created"
    record = store.read_record(CORE, PENDING_KEY)
    assert set(record["method"]["structure"]) == {
        "groupsAdded",
        "groupsRemoved",
        "subjectsAddedToExistingNamespaces",
    }
    del record["method"]["structure"]  # claims a comparison but lacks its data
    assert any("method.structure is required" in p for p in record_problems(record))
    record["method"]["structure"] = {"groupsAdded": 1}
    assert any("method.structure needs integer" in p for p in record_problems(record))


def test_extraction_is_reused_only_while_target_and_baseline_are_unchanged(tmp_path, accepted, core):
    store, src = Store(tmp_path / "reg"), pipeline.empty_source_state()
    spec = _release(core, accepted, baseline=core["shas"]["v1.0.0"], baseline_tag="v1.0.0")
    assert _process(store, core, accepted, spec, src) == "created"
    assert _process(store, core, accepted, spec, src) == "unchanged"  # same target, same baseline: fast path
    # The nearest ancestor changes (a tag moved or another release became resolvable); the target is the same.
    other = _release(core, accepted, baseline=core["shas"]["v1.0.1"], baseline_tag="v1.0.1")
    assert other["commit"] == spec["commit"]
    assert _process(store, core, accepted, other, src) == "updated"
    assert store.read_record(CORE, PENDING_KEY)["baselineCommit"] == core["shas"]["v1.0.1"]
    assert _process(store, core, accepted, other, src) == "unchanged"


def test_an_unresolved_baseline_is_reused_until_one_appears_then_refreshed(tmp_path, accepted, core):
    store, src = Store(tmp_path / "reg"), pipeline.empty_source_state()
    assert _process(store, core, accepted, _release(core, accepted), src) == "created"
    assert _process(store, core, accepted, _release(core, accepted), src) == "unchanged"  # still no baseline
    resolved = _release(core, accepted, baseline=core["shas"]["v1.0.0"], baseline_tag="v1.0.0")
    assert _process(store, core, accepted, resolved, src) == "updated"
    record = store.read_record(CORE, PENDING_KEY)
    assert record["baselineCommit"] == core["shas"]["v1.0.0"] and "structure" in record["method"]


def test_a_changed_baseline_does_not_keep_a_no_milestone(tmp_path, accepted, core):
    store, src = Store(tmp_path / "reg"), pipeline.empty_source_state()
    spec = _release(core, accepted, baseline=core["shas"]["v1.0.0"], baseline_tag="v1.0.0")
    _process(store, core, accepted, spec, src)
    record = store.read_record(CORE, PENDING_KEY)
    record["candidates"], record["noMilestone"] = [], {"reason": "nothing worth a milestone"}
    store.write_record(record)
    other = _release(core, accepted, baseline=core["shas"]["v1.0.1"], baseline_tag="v1.0.1")
    assert _process(store, core, accepted, other, src) == "updated"
    refreshed = store.read_record(CORE, PENDING_KEY)
    assert refreshed["noMilestone"] is None  # the new comparison has not been reviewed
    assert refreshed["resetReviews"]["noMilestone"] == {"reason": "nothing worth a milestone"}


def _fail_extraction(monkeypatch):
    def boom(*_args, **_kwargs):
        raise RuntimeError("extraction exploded")

    monkeypatch.setattr(pipeline.revisions, "build_record", boom)


def test_a_failed_first_manual_range_keeps_its_flag_and_never_clears_the_cursor(tmp_path, accepted, genai, monkeypatch):
    store = Store(tmp_path / "reg")
    state, _ = run_genai(store, accepted, genai)
    (done,) = state["revisions"]
    record = store.read_record(GENAI, done)
    record["noMilestone"] = {"reason": "an attribute addition inside an existing namespace"}
    store.write_record(record)
    state = pipeline.refresh_statuses(store, accepted, state)
    assert state["cursor"] == done and state["revisions"][done]["status"] == "complete"

    _fail_extraction(monkeypatch)  # the explicit replay fails the first time it is tried
    failed, report = run_genai(
        store, accepted, genai, state, from_ref=genai["shas"]["start"], to_ref=genai["shas"]["relevant"]
    )
    (manual_key,) = [k for k in failed["revisions"] if k != done]
    entry = failed["revisions"][manual_key]
    assert report["actions"][manual_key] == "failed" and "extraction exploded" in entry["error"]
    assert entry["manual"] is True  # kept: it is an explicit replay, outside the cursor's run
    # The decisive part: the failure must not enter ordinary cursor processing. With no candidate file its date is
    # empty, which would sort first, and the cursor would be cleared.
    assert failed["order"] == [done] and failed["cursor"] == done
    assert failed["revisions"][done]["status"] == "complete"
    refreshed = pipeline.refresh_statuses(store, accepted, failed)
    assert refreshed["order"] == [done] and refreshed["cursor"] == done
    assert refreshed["revisions"][manual_key]["status"] == "failed"  # still visible, never treated as processed

    # The next ordinary run starts after the completed range and leaves its noMilestone alone.
    monkeypatch.undo()
    again, _ = run_genai(store, accepted, genai, refreshed)
    assert again["cursor"] == done
    assert store.read_record(GENAI, done)["noMilestone"] == record["noMilestone"]


def test_a_failed_first_extraction_of_a_published_release_keeps_its_tag(tmp_path, accepted, core, monkeypatch):
    store = Store(tmp_path / "reg")
    _fail_extraction(monkeypatch)
    state, report = run_core(store, accepted, core, tag="v1.1.0")
    key = f"{CORE}@v1.1.0"
    assert report["actions"][key] == "failed"
    assert state["revisions"][key] == {
        "kind": "release",
        "source": CORE,
        "commit": core["shas"]["v1.1.0"],
        "tag": "v1.1.0",
        "error": "extraction exploded",
        "status": "failed",
    }
