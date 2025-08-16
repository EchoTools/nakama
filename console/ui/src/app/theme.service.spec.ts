import { TestBed } from '@angular/core/testing';
import { ThemeService, Theme } from './theme.service';

describe('ThemeService', () => {
  let service: ThemeService;

  beforeEach(() => {
    TestBed.configureTestingModule({});
    service = TestBed.inject(ThemeService);
    // Clear localStorage before each test
    localStorage.clear();
    // Reset body classes
    document.body.className = '';
  });

  afterEach(() => {
    localStorage.clear();
    document.body.className = '';
  });

  it('should be created', () => {
    expect(service).toBeTruthy();
  });

  it('should default to dark theme', () => {
    expect(service.getCurrentTheme()).toBe('dark');
    expect(document.body.classList.contains('theme-dark')).toBe(true);
  });

  it('should toggle theme from dark to light', () => {
    expect(service.getCurrentTheme()).toBe('dark');
    service.toggleTheme();
    expect(service.getCurrentTheme()).toBe('light');
    expect(document.body.classList.contains('theme-light')).toBe(true);
    expect(document.body.classList.contains('theme-dark')).toBe(false);
  });

  it('should toggle theme from light to dark', () => {
    service.setTheme('light');
    expect(service.getCurrentTheme()).toBe('light');
    service.toggleTheme();
    expect(service.getCurrentTheme()).toBe('dark');
    expect(document.body.classList.contains('theme-dark')).toBe(true);
    expect(document.body.classList.contains('theme-light')).toBe(false);
  });

  it('should persist theme in localStorage', () => {
    service.setTheme('light');
    expect(localStorage.getItem('nakama-console-theme')).toBe('light');
    
    service.setTheme('dark');
    expect(localStorage.getItem('nakama-console-theme')).toBe('dark');
  });

  it('should load saved theme from localStorage', () => {
    localStorage.setItem('nakama-console-theme', 'light');
    const newService = new ThemeService();
    expect(newService.getCurrentTheme()).toBe('light');
  });

  it('should apply theme classes to document body', () => {
    service.setTheme('light');
    expect(document.body.classList.contains('theme-light')).toBe(true);
    expect(document.body.classList.contains('theme-dark')).toBe(false);
    
    service.setTheme('dark');
    expect(document.body.classList.contains('theme-dark')).toBe(true);
    expect(document.body.classList.contains('theme-light')).toBe(false);
  });

  it('should emit theme changes via observable', () => {
    const themes: Theme[] = [];
    service.currentTheme$.subscribe(theme => themes.push(theme));
    
    service.setTheme('light');
    service.setTheme('dark');
    
    expect(themes).toEqual(['dark', 'light', 'dark']); // Initial + 2 changes
  });
});