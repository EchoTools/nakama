# Custom Agent Prompts

This directory contains custom agent prompts for GitHub Copilot and other AI coding assistants working on the EchoTools/Nakama repository.

## Available Agents

### golang-expert.md
Expert Golang developer specialized in:
- EchoVR binary protocol implementation
- Nakama game server framework
- Matchmaking and skill-based systems
- Discord bot integration
- PostgreSQL database operations

**Use for**:
- Go code changes in `server/` directory
- EVR protocol message handlers
- RPC endpoint implementation
- Matchmaker modifications
- Discord bot command development

## Usage

### With GitHub Copilot
1. Reference the agent prompt in your issue or PR description
2. Copilot will use the context from the agent prompt to provide better suggestions

### With Custom Agents
1. Copy the contents of the agent prompt
2. Use it as system instructions for your custom AI agent
3. The agent will follow the conventions and patterns specified

### As Developer Reference
- Read through the agent prompts to understand codebase conventions
- Use as a reference guide for new contributors
- Keep updated as conventions evolve

## Structure

Each agent prompt includes:
- **Specialization** - Domain and technology focus
- **Core Components** - Key files and directories
- **Code Conventions** - Mandatory coding patterns
- **Testing** - How to run tests efficiently
- **Build & Run** - Local development setup
- **Commits** - Conventional commit format rules
- **Task Workflow** - Step-by-step development process
- **Key Libraries** - Important dependencies
- **Security** - Security considerations

## Updating

When codebase conventions change:
1. Update the relevant agent prompt
2. Update the main documentation (`GOLANG_AGENT_PROMPT.md`)
3. Commit with `docs(agents): <description>`

## Related Documentation

- `/GOLANG_AGENT_PROMPT.md` - Comprehensive Golang development guide
- `/.github/copilot-instructions.md` - GitHub Copilot specific instructions
- `/README.md` - Main repository documentation
