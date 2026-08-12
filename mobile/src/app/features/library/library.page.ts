import { Component, ChangeDetectionStrategy } from '@angular/core';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { LibraryBrowserComponent } from './library-browser';

/**
 * The Library as a destination: the same store the Skills and MCP pages open as a dialog, with the
 * workspace header above it. Browsing a catalog deserves a place of its own — you are not always
 * here because something is missing — but there must not be two stores, so this page is nothing but
 * a frame around the one component.
 */
@Component({
  selector: 'app-library',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [WorkspaceHeaderComponent, LibraryBrowserComponent],
  template: `
    <app-workspace-header />
    <app-library-browser />
  `,
})
export class LibraryPage {}
