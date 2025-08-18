# EchoTools Nakama Server

[![License](https://img.shields.io/github/license/heroiclabs/nakama.svg)](https://github.com/heroiclabs/nakama/blob/master/LICENSE)

> Customized Nakama game server for Echo VR with Discord integration and enhanced matchmaking capabilities.

EchoTools Nakama is a specialized fork of the [Nakama game server](https://github.com/heroiclabs/nakama) specifically designed for Echo VR gameplay. It includes extensive Discord bot integration, game server registration features, and VR-specific matchmaking capabilities.

## Features

* **Discord Integration** - Full Discord bot with slash commands, user linking, and community management
* **Echo VR Support** - Native support for Echo VR game servers and matchmaking
* **Real-time Multiplayer** - Optimized for VR gameplay with low-latency networking
* **User Authentication** - Discord-based authentication and user management
* **Game Server Registry** - Automatic registration and management of Echo VR game servers
* **Advanced Matchmaking** - Custom matchmaking algorithms for VR gameplay
* **Web Console** - Administrative interface for server management and monitoring

For complete feature documentation, see the [upstream Nakama documentation](https://heroiclabs.com/docs/).

## Documentation

* **[Game Server URL Parameters](docs/game-server-url-parameters.md)** - Complete reference for URL parameters used in game server registration.

## Prerequisites

Before setting up EchoTools Nakama, ensure you have the following:

### Required Software

- **Docker and Docker Compose** - For containerized deployment
- **Git** - For cloning the repository

### Discord Bot Setup

EchoTools Nakama requires a Discord bot for user authentication and community features:

1. **Create a Discord Application**:
   - Go to the [Discord Developer Portal](https://discord.com/developers/applications)
   - Click "New Application" and give it a name
   - Navigate to the "Bot" section and create a bot
   - Copy the bot token (you'll need this for `DISCORD_BOT_TOKEN`)

2. **Configure Bot Permissions**:
   - In the Discord Developer Portal, go to the "OAuth2" > "URL Generator" section
   - Select "bot" and "applications.commands" scopes
   - Select the following bot permissions:
     - Send Messages
     - Use Slash Commands
     - Read Message History
     - Manage Messages
     - Connect
     - Speak
   - Use the generated URL to invite the bot to your Discord server

3. **Get Discord Server ID**:
   - Enable Developer Mode in Discord (User Settings > Advanced > Developer Mode)
   - Right-click your Discord server and select "Copy Server ID"

## Quick Start with Docker Compose

The fastest way to get EchoTools Nakama running locally is with the included Docker Compose configuration.

### 1. Clone the Repository

```bash
git clone https://github.com/EchoTools/nakama.git
cd nakama
```

### 2. Configure Environment Variables

Create a `.env` file in the project root with your Discord bot configuration:

```bash
# Required: Discord Bot Token (the server requires this to function)
DISCORD_BOT_TOKEN=your_discord_bot_token_here

# Optional: Additional configuration
NAKAMA_TELEMETRY=0  # Disable telemetry
```

**Important:** EchoTools Nakama requires a valid Discord bot token to run. See the [Discord Bot Setup](#discord-bot-setup) section above for instructions on creating a Discord bot.

### 3. Start the Services

```bash
# Start PostgreSQL database and Nakama server
docker compose up -d

# View logs (optional)
docker compose logs -f nakama
```

This will:
- Start a PostgreSQL database on port 5432
- Run database migrations automatically
- Start the Nakama server with Discord integration enabled
- Expose the following services:
  - **API Server**: http://127.0.0.1:7350
  - **WebSocket**: ws://127.0.0.1:7349  
  - **Console**: http://127.0.0.1:7351

### 4. Verify the Installation

Test the API server:

```bash
curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
  --user "defaultkey:" \
  --data '{"id": "test-device-123"}'
```

You should receive a JSON response with an authentication token.

## Local Development Setup

For development work, you may want to build and run the server locally instead of using Docker.

### Prerequisites for Local Development

- **Go 1.25+** - Required for building the server
- **Docker** - For running PostgreSQL database  
- **Make** - For using the build system

### 1. Setup Dependencies

```bash
# Clone repository
git clone https://github.com/EchoTools/nakama.git
cd nakama

# Download Go dependencies (~1 minute)
go mod vendor

# Build the server (~1 minute)  
make nakama
```

### 2. Start Database

```bash
# Start PostgreSQL database (~30 seconds)
docker compose up -d postgres

# Wait for database to be ready
sleep 30

# Run database migrations
./nakama-debug migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
```

### 3. Configure Environment

Set your Discord bot token:

```bash
export DISCORD_BOT_TOKEN="your_discord_bot_token_here"
```

### 4. Start the Server

```bash
# Set your Discord bot token
export DISCORD_BOT_TOKEN="your_discord_bot_token_here"

# Start Nakama server
./nakama-debug --name nakama1 \
  --database.address postgres:localdb@127.0.0.1:5432/nakama \
  --logger.level INFO
```

**Note:** The Discord bot token is required for the server to start successfully. Without it, the server will not function properly due to the integrated Discord features.

## Configuration

### Required Environment Variables

- **`DISCORD_BOT_TOKEN`** - Discord bot token for authentication and community features (required)

### Optional Environment Variables

- **`NAKAMA_TELEMETRY`** - Set to `0` to disable telemetry (default: enabled)

### Configuration Files

EchoTools Nakama can be configured via YAML files. Create a `nakama.yml` file in the `data/` directory for custom configuration:

```yaml
# Example nakama.yml
name: "EchoTools-Nakama"
logger:
  level: "INFO"
  format: "JSON"
session:
  token_expiry_sec: 7200
socket:
  server_key: "defaultkey"
```

For complete configuration options, see the [Nakama Configuration Documentation](https://heroiclabs.com/docs/nakama/getting-started/configuration/).

## Server Management

### Accessing the Admin Console

The Nakama Console provides a web interface for managing users, data, and server metrics:

1. Navigate to http://127.0.0.1:7351 in your browser
2. Log in with the default credentials or configure authentication

### Common Commands

```bash
# View server status
docker compose ps

# View server logs
docker compose logs -f nakama

# Restart the server
docker compose restart nakama

# Stop all services
docker compose down

# Start with fresh database
docker compose down -v
docker compose up -d
```

### Health Checks

The server includes built-in health check endpoints:

```bash
# Check server health
curl http://127.0.0.1:7350/v2/healthcheck

# Check using the binary
./nakama-debug healthcheck
```

## Game Server Integration

EchoTools Nakama includes specialized features for Echo VR game servers:

### Game Server Registration

Echo VR game servers can automatically register with Nakama using WebSocket connections with specific URL parameters. See [Game Server URL Parameters](docs/game-server-url-parameters.md) for complete documentation.

### Discord Authentication

Game servers can authenticate using Discord credentials:

```
wss://your-nakama-server.com/ws?discordid=123456789012345678&password=yourpassword
```

## API Usage

EchoTools Nakama supports the full Nakama API with additional VR-specific endpoints. The server supports both REST and GRPC protocols.

### Authentication Example

```bash
curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
  --user "defaultkey:" \
  --data '{"id": "someuniqueidentifier"}'
```

Response:
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

For complete API documentation, see the [Nakama API Reference](https://heroiclabs.com/docs/nakama-api/).

### Client Libraries

Use the official [Nakama client libraries](https://github.com/heroiclabs) which are compatible with EchoTools Nakama:

- **Unity** - For VR game development
- **Unreal Engine** - For VR applications  
- **JavaScript/TypeScript** - For web interfaces
- **.NET (C#)** - For desktop applications
- **Java** - For Android VR applications

## Troubleshooting

### Common Issues

**Discord bot not responding:**
- Verify the bot token is correct and properly set in the environment
- Check the bot has proper permissions in your Discord server
- Ensure the Discord bot is online and accessible
- Review server logs: `docker compose logs nakama`

**Server fails to start:**
- Ensure you have set a valid `DISCORD_BOT_TOKEN` environment variable
- Verify PostgreSQL is running: `docker compose ps`
- Check the Discord bot token format is correct
- Ensure ports 7349, 7350, 7351 are available

**Database connection errors:**
- Ensure PostgreSQL container is healthy: `docker compose ps`
- Wait for database initialization: `sleep 30` after starting postgres
- Check database logs: `docker compose logs postgres`

### Getting Help

For additional support:
- Review the [upstream Nakama documentation](https://heroiclabs.com/docs/)
- Check existing [GitHub issues](https://github.com/EchoTools/nakama/issues)
- Join the [Heroic Labs community forum](https://forum.heroiclabs.com)

## Development

### Building from Source

EchoTools Nakama includes all dependencies as part of the Go project using Go modules.

```bash
# Clone the repository
git clone https://github.com/EchoTools/nakama.git
cd nakama

# Download dependencies
go mod vendor

# Build for development (with debug symbols)
make nakama

# Build Docker image
make build

# Verify build
./nakama-debug --version
```

### Testing

Run the EchoTools-specific test suite:

```bash
# Run EVR-specific unit tests
go test -short -vet=off ./server/evr/...

# Run all EVR-related tests
go test -short -vet=off ./server -run ".*evr.*"
```

For integration testing with database:

```bash
# Start test environment
docker compose -f ./docker-compose-tests.yml up --build --abort-on-container-exit

# Clean up
docker compose -f ./docker-compose-tests.yml down -v
```

### Code Formatting

```bash
# Check formatting
gofmt -l .

# Fix formatting
gofmt -w .
```

## Contributing

We welcome contributions to EchoTools Nakama! This project builds upon the excellent foundation provided by [Heroic Labs' Nakama](https://github.com/heroiclabs/nakama).

### Development Workflow

1. Fork the repository
2. Create a feature branch
3. Make your changes with tests
4. Run formatting and tests
5. Submit a pull request

### Upstream Relationship

This project is a specialized fork of Nakama. For general Nakama features and documentation, refer to the [upstream project](https://github.com/heroiclabs/nakama). EchoTools-specific features are documented in this repository.

## License

This project is licensed under the [Apache-2 License](https://github.com/heroiclabs/nakama/blob/master/LICENSE), the same as the upstream Nakama project.
