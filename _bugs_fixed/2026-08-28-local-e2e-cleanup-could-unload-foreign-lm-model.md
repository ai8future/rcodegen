# Local E2E cleanup could unload a foreign LM Studio model

The new guarded E2E harness initially restored a server it had started before verifying LM Studio's loaded-model state. If another process loaded a model during the test, stopping that server could unload a model the harness did not own.

Cleanup now verifies the loaded state first. It restores the server to stopped only when LM Studio is empty; an unavailable or non-empty state fails the run and leaves the server running so foreign models are preserved.
