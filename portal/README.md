# Portal

This directory contains the source code for the Developer Portal web interface. It consists of a Vue.js frontend that is compiled and embedded into a Go package for distribution.

## Project Structure

- **`ui.go`**: A Go file that declares the `developerportal` package. It uses `//go:embed` to serve the static assets generated in `ui/dist`.
- **`ui/`**: The source code for the Vue.js 3 frontend application, built with Vite and Tailwind CSS.

## Features

Based on the view structure, this portal provides:
- **Authentication**: Discord OAuth integration.
- **Player Management**: Tools for looking up player information.
- **Application Management**: Views for managing applications.

## Development

### Frontend

To work on the user interface, navigate to the `ui` directory:

```bash
# Change to the ui directory
cd ui

# Install dependencies:
npm install

# Start the development server with hot-reload:
npm run dev
```

### Building for Go Embed

To update the static files served by `ui.go`, you must build the frontend. The Go embedding expects the build artifacts to be present in `ui/dist`.

```bash
cd ui
npm run build
```

This command generates the static assets (HTML, CSS, JS) which are then included in the Go binary at compile time.

## Technologies

- **Frontend**: Vue.js 3, Vite, Vue Router
- **Styling**: Tailwind CSS

