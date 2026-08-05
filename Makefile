# This project uses `just` instead of make.
# Please run your commands through just:
#
#   just              # builds nakama (debug)
#   just test         # run all server tests
#   just build        # docker build
#   just release      # docker buildx push
#   just --list       # see all targets
#
# If you need just: https://github.com/casey/just

.PHONY: all
all:
	@echo "This project uses 'just' instead of make."
	@echo "Run: just"
	@exit 1
