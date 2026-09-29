# CLA acceptances

This branch holds the record of who has accepted the KubeMG Contributor Licence
Agreement ([CLA.md](https://github.com/kubemg/kubemg/blob/master/CLA.md)). It is
written by the `CLA` workflow on master (`.github/workflows/cla.yml`): each
acceptance is a commit adding an entry to `signatures/cla.json`, naming the
contributor, the pull request, and the comment in which they accepted.

It is kept apart from master because the bot commits to it directly, and master
takes changes only through review. Do not merge it into master, and do not
rewrite its history — the commits are the record.
