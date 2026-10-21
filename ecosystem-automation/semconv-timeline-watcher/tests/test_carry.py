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
#

"""Pending candidates on the automation branch survive a later discovery run (read as data, validated)."""

import json
import shutil

import pytest
from semconv_timeline_watcher import carry
from semconv_timeline_watcher import main as cli
from semconv_timeline_watcher.gitrepo import GitError
from semconv_timeline_watcher.state import Store
from watcher_common.testing import git_commit, init_repo, run_git

from .conftest import CORE, RELEASES, FakeClient, run_core

BRANCH = "otelbot/automated-semconv-timeline-core"
PENDING = f"{CORE}@v1.1.0"
REGISTRY = f"ecosystem-registry/semantic-conventions/{CORE}"


def _commit_all(path, message):
    run_git(path, "add", "-A")
    git_commit(path, message)


@pytest.fixture
def pending_record(tmp_path, accepted, core):
    """A real, valid candidate record for the v1.1.0 release."""
    store = Store(tmp_path / "gen")
    run_core(store, accepted, core)
    return store.read_record(CORE, PENDING)


@pytest.fixture
def workspace(tmp_path, monkeypatch):
    """A checkout of the default branch whose `origin` also holds the automation branch."""
    origin = tmp_path / "origin.git"
    origin.mkdir()
    run_git(origin, "init", "--bare", "--initial-branch=main")
    work = tmp_path / "work"
    work.mkdir()
    init_repo(work)
    run_git(work, "checkout", "-b", "main")
    (work / "README.md").write_text("base\n")
    _commit_all(work, "base")
    run_git(work, "remote", "add", "origin", str(origin))
    run_git(work, "push", "origin", "main")
    monkeypatch.chdir(work)
    return work


def _publish_branch(work, files):
    """Create the automation branch on origin with *files*, then return to a clean default branch."""
    run_git(work, "checkout", "-b", BRANCH)
    for path, text in files.items():
        target = work / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)
    _commit_all(work, "automation")
    run_git(work, "push", "origin", BRANCH)
    run_git(work, "checkout", "main")
    shutil.rmtree(work / "ecosystem-registry", ignore_errors=True)
    run_git(work, "update-ref", "-d", f"refs/remotes/origin/{BRANCH}")  # as on a fresh runner: nothing fetched yet


def _record_file(record):
    return f"{REGISTRY}/candidates/v1.1.0.json", json.dumps(record, indent=2, sort_keys=True) + "\n"


def test_a_pending_candidate_on_the_branch_is_carried_into_the_working_tree(workspace, accepted, pending_record):
    path, text = _record_file(pending_record)
    _publish_branch(workspace, {path: text})
    store = Store(workspace / REGISTRY)
    carried = carry.carry_pending(store, CORE, BRANCH, accepted)
    assert carried == {
        PENDING: {"kind": "release", "source": CORE, "commit": pending_record["commit"], "tag": "v1.1.0"}
    }
    assert store.read_record(CORE, PENDING) == pending_record
    # Idempotent: a second pass finds it present and changes nothing.
    assert carry.carry_pending(store, CORE, BRANCH, accepted) == {}


def test_a_missing_branch_carries_nothing_but_a_failing_remote_is_an_error(workspace, accepted):
    store = Store(workspace / REGISTRY)
    assert carry.carry_pending(store, CORE, BRANCH, accepted) == {}  # no automation branch yet: first run
    run_git(workspace, "remote", "set-url", "origin", str(workspace / "no-such-remote"))
    with pytest.raises(GitError, match="cannot list"):
        carry.carry_pending(store, CORE, BRANCH, accepted)  # never treated as "nothing pending"


def test_a_record_already_merged_on_the_default_branch_is_not_replaced(workspace, accepted, pending_record):
    path, text = _record_file(pending_record)
    _publish_branch(workspace, {path: text})
    store = Store(workspace / REGISTRY)
    merged = {**pending_record, "noMilestone": {"reason": "reviewed on the default branch"}, "candidates": []}
    store.write_record(merged)
    assert carry.carry_pending(store, CORE, BRANCH, accepted) == {}
    assert store.read_record(CORE, PENDING)["noMilestone"] == {"reason": "reviewed on the default branch"}


def test_a_release_accepted_in_the_meantime_is_not_carried(workspace, accepted, pending_record):
    path, text = _record_file(pending_record)
    _publish_branch(workspace, {path: text})
    accepted.releases[PENDING] = {"key": PENDING}
    assert carry.carry_pending(Store(workspace / REGISTRY), CORE, BRANCH, accepted) == {}


def test_invalid_foreign_or_mismatched_files_on_the_branch_are_skipped_not_trusted(workspace, accepted, pending_record):
    broken = json.loads(json.dumps(pending_record))
    del broken["candidates"][0]["evidence"][0]["url"]
    foreign = {**pending_record, "source": "semantic-conventions-genai"}
    renamed = {**pending_record, "reviewKey": f"{CORE}@v9.9.9"}
    base = f"{REGISTRY}/candidates"
    files = {
        f"{base}/v1.1.0.json": json.dumps(broken),
        f"{base}/foreign.json": json.dumps(foreign),
        f"{base}/renamed.json": json.dumps(renamed),
        f"{base}/garbage.json": "{not json",
        f"{base}/notes.txt": "ignored",
    }
    _publish_branch(workspace, files)
    store = Store(workspace / REGISTRY)
    assert carry.carry_pending(store, CORE, BRANCH, accepted) == {}
    assert not (workspace / REGISTRY / "candidates").exists()  # nothing was written for any of them


def _run_args(workspace, *extra):
    return cli.build_parser().parse_args(
        ["--registry-dir", str(workspace / "ecosystem-registry/semantic-conventions"), "run", *extra]
    )


def test_a_pending_manual_replay_survives_a_later_discovery_run(workspace, tmp_path, accepted, core, monkeypatch):
    """The reported defect: a run that finds nothing new must not drop an unmerged candidate from the branch."""
    store_gen = Store(tmp_path / "gen2")
    run_core(store_gen, accepted, core, tag="v1.1.0")
    key = f"{CORE}@v1.1.0"
    replay = store_gen.read_record(CORE, key)
    path, text = _record_file(replay)
    assert replay["candidates"], "the fixture release should have candidates to review"
    _publish_branch(workspace, {path: text})

    monkeypatch.setenv("GITHUB_REPOSITORY", "o/r")
    args = _run_args(workspace, "--source", CORE, "--carry-branch", BRANCH)
    # Only the older release is discoverable now, so this run produces nothing for v1.1.0 itself.
    client = FakeClient([r for r in RELEASES if r["tag_name"] != "v1.1.0"])
    code = cli.run_command(args, [CORE], accepted, client=client, repos={CORE: core["git"]}, head_ref="HEAD")
    assert code == 0
    store = Store(workspace / "ecosystem-registry/semantic-conventions" / CORE)
    assert store.read_record(CORE, key) == replay  # still there, byte-equivalent
    state = store.load_state()["sources"][CORE]
    assert key in state["revisions"] and state["revisions"][key]["status"] == "pending"
    assert key in state["order"]


def test_carrying_is_opt_in(workspace):
    assert _run_args(workspace, "--source", CORE).carry_branch is None
    assert _run_args(workspace, "--carry-branch", BRANCH).carry_branch == BRANCH
