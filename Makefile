# This project uses `just` instead of make.
# Please run your commands through just:
#
#   just              # builds nakama (debug)
#   just test         # run the DB-free server test suite
#   just test-db      # run the full suite, including DB-backed tests
#   just build        # docker build
#   just release      # docker buildx push
#   just --list       # see all targets
#
# If you need just: https://github.com/casey/just
#
# Every target below is a redirect that prints the message and exits NON-ZERO.
# The catch-all `%:` rule covers target names not listed explicitly.
#
# .PHONY is essential: `build/` and `test/` are tracked directories and `nakama`
# is the built binary name, so without it make treats them as up-to-date file
# targets, prints "Nothing to be done", and exits 0 -- a silent false success.

define redirect
@echo "This project uses 'just' instead of make." >&2
@echo "'make $@' does nothing. Run 'just $@' instead, or 'just --list' to see all recipes." >&2
@exit 1
endef

.PHONY: all nakama build test test-db release
all nakama build test test-db release:
	$(redirect)

%:
	$(redirect)
