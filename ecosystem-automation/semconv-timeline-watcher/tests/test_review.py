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

"""Review issue content, stable keying and idempotent upserts."""

import pytest
from semconv_timeline_watcher import pipeline, review, revisions
from semconv_timeline_watcher.state import Store

from .conftest import CORE, GENAI, FakeClient, commit, run_core, run_genai, write


def _items(store, accepted, state, source, coverage=None):
    return [
        {
            "kind": "revision",
            "key": key,
            "source": source,
            "record": store.read_record(source, key),
            "status": revision["status"],
            "state": revision,
            "coverage": coverage or {},
        }
        for key, revision in state["revisions"].items()
    ]


def test_review_body_gives_a_maintainer_everything_needed_to_decide(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    key = f"{CORE}@v1.1.0"
    body = review.render_body(
        store.read_record(CORE, key), "pending", accepted, state["revisions"][key], "ecosystem-registry/x.json"
    )
    assert body.startswith(review.marker(key))
    for expected in [
        core["shas"]["v1.1.0"],
        core["shas"]["v1.0.0"],
        "release-published-at",
        "2026-02-02",
        "/compare/",
        "- [ ] `semantic-conventions@v1.1.0/stability/k8s/stable`",
        "1 of 3 subjects",
        "/issues/10",
        "Extraction gaps and ambiguities",
        "ambiguity",
        "Record a disposition",
        '"status": "accepted"',
        '"status": "excluded"',
        "noMilestone",
        "timeline.json",
        "uv run semconv-timeline-watcher check",
        "bun run test",
        "bun run serve",
        "does not move it",
        "Closing this issue does not complete the review",
        "`http-new`",
    ]:
        assert expected in body, expected
    assert "bun run dev" not in body  # the frontend defines `serve`, not `dev`
    assert body == review.render_body(
        store.read_record(CORE, key), "pending", accepted, state["revisions"][key], "ecosystem-registry/x.json"
    )


def test_genai_review_lists_commits_and_the_coverage_it_does_not_advance(tmp_path, accepted, genai):
    store = Store(tmp_path / "reg")
    state, _ = run_genai(store, accepted, genai)
    (key,) = state["revisions"]
    body = review.render_body(store.read_record(GENAI, key), "pending", accepted, state["revisions"][key], "f.json")
    upstream = accepted.sources[GENAI]["repository"].removeprefix("https://github.com/")
    assert "commit-date" in body and f"Add prompt versioning ({upstream}#137)" in body
    assert genai["shas"]["start"][:12] in body
    assert "No candidates were extracted" in body and "not** a verified absence" in body


def test_an_incomplete_extraction_is_never_presented_as_no_change(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core, tag="v1.0.1")
    key = f"{CORE}@v1.0.1"
    record = store.read_record(CORE, key)
    record["gaps"].append({"kind": "incomplete", "message": "2 model file(s) could not be parsed"})
    body = review.render_body(record, "partial", accepted, state["revisions"][key], "f.json")
    assert "**partial**" in body and "could not be parsed" in body


def test_upserts_are_keyed_idempotent_and_reopen_incomplete_reviews(tmp_path, accepted, core):
    store, client = Store(tmp_path / "reg"), FakeClient()
    state, _ = run_core(store, accepted, core)
    items = _items(store, accepted, state, CORE)
    first = review.upsert_reviews(client, "o/r", items, accepted)
    assert sorted(a for _, a in first) == ["created", "created"] and len(client.issues) == 2
    again = review.upsert_reviews(client, "o/r", items, accepted)
    assert {a for _, a in again} == {"unchanged"} and len(client.issues) == 2  # no duplicates on rerun
    client.issues[0]["state"] = "closed"
    reopened = review.upsert_reviews(client, "o/r", items, accepted)
    assert client.issues[0]["state"] == "open" and "reopened" in {a for _, a in reopened}
    assert any(c[0] == "comment" and "still needs a maintainer" in c[2] for c in client.calls)
    items[0]["record"]["gaps"].append({"kind": "info", "message": "new evidence"})
    assert "updated" in {a for _, a in review.upsert_reviews(client, "o/r", items, accepted)}
    assert {i["labels"][0] for i in client.issues} == {review.LABEL}


def test_completed_revisions_get_no_issue(tmp_path, accepted, core):
    store, client = Store(tmp_path / "reg"), FakeClient()
    state, _ = run_core(store, accepted, core, tag="v1.0.1")
    record = store.read_record(CORE, f"{CORE}@v1.0.1")
    record["noMilestone"] = {"reason": "code generation only"}
    store.write_record(record)
    state = pipeline.refresh_statuses(store, accepted, state)
    assert review.upsert_reviews(client, "o/r", _items(store, accepted, state, CORE), accepted) == []
    assert client.issues == []


def test_the_issue_shows_the_first_ten_subjects_while_the_record_keeps_all(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core)
    key = f"{CORE}@v1.1.0"
    record = store.read_record(CORE, key)
    record["candidates"][0]["subjects"] = [f"ns.attr{i:02d}" for i in range(15)]
    body = review.render_body(record, "pending", accepted, state["revisions"][key], "ecosystem-registry/x.json")
    assert "`ns.attr09`" in body and "`ns.attr10`" not in body and "and 5 more" in body
    assert len(record["candidates"][0]["subjects"]) == 15  # rendering never shortens the stored list


UP = "open-telemetry/semantic-conventions-genai"


@pytest.mark.parametrize(
    ("text", "expected"),
    [
        ("Sample agent.name (#107)", f"Sample agent.name ({UP}#107)"),
        ("#107 and #108", f"{UP}#107 and {UP}#108"),
        ("fixes #7, see also #8.", f"fixes {UP}#7, see also {UP}#8."),
        (f"already {UP}#107", f"already {UP}#107"),  # already names a repository
        ("other/repo#5", "other/repo#5"),
        ("`#107` stays in code", "`#107` stays in code"),
        ("code `#1` then #2", f"code `#1` then {UP}#2"),
        ("https://example.test/page#107", "https://example.test/page#107"),  # a URL fragment
        ("https://github.com/o/r/issues/1#issuecomment-5", "https://github.com/o/r/issues/1#issuecomment-5"),
        ("&#107; entity", "&#107; entity"),
        ("# heading and C# and #hashtag", "# heading and C# and #hashtag"),  # not issue numbers
        ("", ""),
    ],
)
def test_upstream_issue_references_name_their_repository(text, expected):
    assert review.qualify(text, UP) == expected


def test_safe_qualifies_only_when_the_text_is_upstream():
    assert review.safe("fix (#5)") == "fix (#5)"  # local or generated text is untouched
    assert review.safe("fix (#5) @user", UP) == f"fix ({UP}#5) @\u200buser"  # still neutralises mentions


def test_upstream_commit_subjects_and_gap_text_do_not_become_local_references(tmp_path, accepted, genai):
    store = Store(tmp_path / "reg")
    state, _ = run_genai(store, accepted, genai)
    (key,) = state["revisions"]
    record = store.read_record(GENAI, key)
    record["relevantCommits"][0]["subject"] = "Sample `agent.name` (#107) `#9`"
    record["gaps"].append({"kind": "ambiguity", "message": "Changelog entry 'rename (#12)' may be a reorganisation."})
    record["candidates"] = [
        {**_candidate_like(), "reasons": ["see #3"], "warnings": ["Requires human review.", "also #4"]}
    ]
    body = review.render_body(record, "pending", accepted, state["revisions"][key], "f.json")
    assert f"Sample `agent.name` ({UP}#107) `#9`" in body  # code spans keep their text
    assert f"rename ({UP}#12)" in body and f"see {UP}#3" in body and f"also {UP}#4" in body
    assert "(#107)" not in body and "(#12)" not in body and " #3" not in body.replace(f"{UP}#3", "")


def test_each_source_qualifies_with_its_own_repository(tmp_path, accepted, core):
    store = Store(tmp_path / "reg")
    state, _ = run_core(store, accepted, core, tag="v1.0.1")
    key = f"{CORE}@v1.0.1"
    record = store.read_record(CORE, key)
    record["gaps"].append({"kind": "ambiguity", "message": "mentions (#55)"})
    core_slug = accepted.sources[CORE]["repository"].removeprefix("https://github.com/")
    body = review.render_body(record, "pending", accepted, state["revisions"][key], "f.json")
    assert f"mentions ({core_slug}#55)" in body and "semantic-conventions-genai#55" not in body


def _candidate_like():
    return {
        "id": "x/stability/ns/stable",
        "kind": "stability",
        "proposedType": "stability",
        "lane": None,
        "namespace": "ns",
        "transition": "ns: selected subjects",
        "scope": {"changed": 1, "total": 1},
        "fromStates": {"development": 1},
        "subjects": ["ns.a"],
        "evidence": [],
        "disposition": None,
    }


def _range_record(commits, count):
    return {
        "kind": "range",
        "relevantCommits": [{"commit": f"{i:040x}", "subject": f"subject {i}"} for i in range(commits)],
        "method": {"relevantCommitCount": count} if count is not None else {},
    }


@pytest.mark.parametrize(
    ("listed", "total", "heading"),
    [
        (0, 0, "### Commits (0 relevant)"),
        (3, 3, "### Commits (3 relevant)"),
        (50, 50, "### Commits (50 relevant)"),  # exactly at the limit: nothing is hidden
        (50, 76, "### Commits (showing 50 of 76 relevant, oldest first)"),
        (50, 51, "### Commits (showing 50 of 51 relevant, oldest first)"),
        (3, None, "### Commits (3 relevant)"),  # an older record without a count
    ],
)
def test_the_commit_heading_states_the_total_and_how_many_are_listed(tmp_path, accepted, genai, listed, total, heading):
    store = Store(tmp_path / "reg")
    state, _ = run_genai(store, accepted, genai)
    (key,) = state["revisions"]
    record = {**store.read_record(GENAI, key), **_range_record(listed, total)}
    body = review.render_body(record, "pending", accepted, state["revisions"][key], "f.json")
    assert heading in body
    listed_lines = [line for line in body.splitlines() if line.startswith("- `0000")]
    assert len(listed_lines) == listed  # exactly the stored commits are listed, never more
    hidden = (total or listed) - listed
    assert (f"{hidden} newer relevant commit(s) are not listed" in body) == (hidden > 0)


def test_the_stored_count_covers_the_whole_range_and_the_list_keeps_the_oldest_commits(tmp_path, accepted, genai):
    path, mp = genai["path"], genai["monkeypatch"]
    for i in range(55):
        write(path, f"docs/gen-ai/page{i:02d}.md", f"{i}\n")
        commit(path, f"docs {i:02d}", f"2026-07-01T00:{i:02d}:00+00:00", mp)
    store = Store(tmp_path / "reg")
    state, _ = run_genai(store, accepted, genai)
    (key,) = state["revisions"]
    record = store.read_record(GENAI, key)
    assert record["method"]["relevantCommitCount"] == 56  # the original relevant commit plus 55
    assert len(record["relevantCommits"]) == revisions._MAX_COMMITS == 50
    subjects = [c["subject"] for c in record["relevantCommits"]]
    assert subjects[0] == "Add prompt versioning (#137)" and subjects[-1] == "docs 48"  # oldest first, newest cut
    body = review.render_body(record, "pending", accepted, state["revisions"][key], "f.json")
    assert "showing 50 of 56 relevant, oldest first" in body


def test_a_completed_revisions_issue_is_left_for_a_maintainer_to_close(tmp_path, accepted, core):
    store, client = Store(tmp_path / "reg"), FakeClient()
    state, _ = run_core(store, accepted, core)
    items = _items(store, accepted, state, CORE)
    review.upsert_reviews(client, "o/r", items, accepted)
    before = [dict(issue) for issue in client.issues]
    calls = list(client.calls)
    for item in items:
        item["status"] = "complete"  # processing is finished; acceptance is a separate, human step
    assert review.upsert_reviews(client, "o/r", items, accepted) == []  # nothing is touched, nothing is closed
    assert client.issues == before and client.calls == calls
    assert all(issue["state"] == "open" for issue in client.issues)
