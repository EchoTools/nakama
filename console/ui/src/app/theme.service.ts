// Copyright 2020 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { Injectable } from '@angular/core';
import { BehaviorSubject } from 'rxjs';

export type Theme = 'light' | 'dark';

@Injectable({
  providedIn: 'root'
})
export class ThemeService {
  private readonly THEME_STORAGE_KEY = 'nakama-console-theme';
  private readonly DEFAULT_THEME: Theme = 'dark'; // Dark mode by default

  private currentThemeSubject = new BehaviorSubject<Theme>(this.DEFAULT_THEME);
  public currentTheme$ = this.currentThemeSubject.asObservable();

  constructor() {
    this.initializeTheme();
  }

  private initializeTheme(): void {
    const savedTheme = this.getSavedTheme();
    const theme = savedTheme || this.DEFAULT_THEME;
    this.setTheme(theme);
  }

  private getSavedTheme(): Theme | null {
    try {
      const saved = localStorage.getItem(this.THEME_STORAGE_KEY);
      return saved === 'light' || saved === 'dark' ? saved : null;
    } catch {
      return null;
    }
  }

  private saveTheme(theme: Theme): void {
    try {
      localStorage.setItem(this.THEME_STORAGE_KEY, theme);
    } catch {
      // localStorage not available, continue without saving
    }
  }

  public setTheme(theme: Theme): void {
    this.currentThemeSubject.next(theme);
    this.saveTheme(theme);
    this.applyThemeToDocument(theme);
  }

  public toggleTheme(): void {
    const currentTheme = this.currentThemeSubject.value;
    const newTheme: Theme = currentTheme === 'light' ? 'dark' : 'light';
    this.setTheme(newTheme);
  }

  public getCurrentTheme(): Theme {
    return this.currentThemeSubject.value;
  }

  private applyThemeToDocument(theme: Theme): void {
    const bodyElement = document.body;
    
    // Remove existing theme classes
    bodyElement.classList.remove('theme-light', 'theme-dark');
    
    // Add new theme class
    bodyElement.classList.add(`theme-${theme}`);
  }
}