# git-pending
A binary to list out pending git repos locally.

## Install

```sh
go install github.com/mvndaai/git-pending@latest
```

Because the binary is named `git-pending`, you can run it as `git-pending` or `git pending`.

## Usage

```
git-pending [filter=PATTERN] [cmd=COMMAND] [skip=stash,untracked] [DIR]
  filter  only repos whose path contains PATTERN (globs allowed)
  cmd     run COMMAND <repo> for each result, e.g. cmd=code
  skip    comma-separated checks to ignore: stash,untracked
  DIR     folder to search (default: .)
options may also be written -opt=VALUE or --opt=VALUE
```

Reasons reported: not committed, untracked files, not pushed, commit(s) not pushed,
stash(es), detached HEAD, rebase in progress, merge in progress.
