# Nakama Console Dark Mode

The Nakama console now supports both light and dark themes, with **dark mode set as the default** to improve user experience and reduce eye strain.

## Features

- **Dark mode by default**: Opens in dark mode to prevent "flashbangs" 
- **Theme toggle**: Easily switch between light and dark themes using the toggle in the sidebar
- **Theme persistence**: Your theme preference is saved and restored across browser sessions
- **Smooth transitions**: Themes switch with smooth 0.3s transitions
- **Accessible design**: Both themes maintain proper color contrast for accessibility
- **Comprehensive theming**: All UI elements (forms, tables, cards, buttons) support both themes

## Usage

### Theme Toggle
- Click the theme toggle button in the sidebar (sun/moon icon)
- **☀️ Light Mode** - Switch to light theme
- **🌙 Dark Mode** - Switch to dark theme

### Default Behavior
- Console opens in dark mode by default
- Theme preference is automatically saved in browser local storage
- Theme persists across browser sessions and page reloads

## Technical Implementation

### Theme Service
The `ThemeService` manages theme state and persistence:
- Reactive theme updates using RxJS BehaviorSubject
- LocalStorage persistence for theme preferences
- Document body class management for theme application

### CSS Custom Properties
The theming system uses CSS custom properties for consistent styling:

```css
/* Dark Theme Colors */
.theme-dark {
  --bg-primary: #1A1B1E;
  --bg-secondary: #2A2D32;
  --text-primary: #E9ECEF;
  --accent-color: #8A7EFF;
  /* ... more properties */
}

/* Light Theme Colors */
.theme-light {
  --bg-primary: #FFFFFF;
  --bg-secondary: #FAFAFC;
  --text-primary: #333564;
  --accent-color: #7668ED;
  /* ... more properties */
}
```

### Component Integration
- `ThemeToggleComponent`: Provides the theme switching UI
- `BaseComponent`: Integrates theme service and toggle into main layout
- Global styles: Use CSS custom properties for consistent theming

## Development

### Adding New Components
When creating new components, use the CSS custom properties for consistent theming:

```scss
.my-component {
  background-color: var(--bg-primary);
  color: var(--text-primary);
  border-color: var(--border-color);
}
```

### Testing
The theme service includes comprehensive unit tests covering:
- Default theme behavior
- Theme toggling
- Persistence
- Observable updates
- DOM class application

## Browser Support

The dark mode implementation uses modern CSS custom properties and is supported in:
- Chrome 49+
- Firefox 31+
- Safari 9.1+
- Edge 16+

## Accessibility

Both themes maintain WCAG AA contrast ratios:
- **Dark theme**: Light text (#E9ECEF) on dark backgrounds
- **Light theme**: Dark text (#333564) on light backgrounds
- **Focus indicators**: Visible focus outlines for keyboard navigation
- **ARIA labels**: Proper labeling for theme toggle button