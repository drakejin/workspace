.PHONY: init sync workspace docs-cleaner run-issues done-issue

init:
	@bash scripts/init/run.sh

sync:
	@bash scripts/sync/run.sh

workspace:
	@bash scripts/sync/_collect_workspace.sh main workspace.json

docs-cleaner:
	@bash scripts/docs-cleaner/run.sh

run-issues:
	@bash scripts/run-issues/run.sh $(ARGS)

done-issue:
	@bash scripts/done-issue/run.sh $(ARGS)
