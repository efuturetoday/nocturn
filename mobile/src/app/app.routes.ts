import { Routes } from '@angular/router';
import { connectionGuard } from './core/guards/connection.guard';

/**
 * Discover → Workspaces → Chats (per workspace) → Chat.
 * `:ws` and `:id` route params bind to component `input()`s via withComponentInputBinding().
 */
export const routes: Routes = [
  { path: '', redirectTo: 'discover', pathMatch: 'full' },
  {
    path: 'discover',
    loadComponent: () => import('./features/discover/discover.page').then((m) => m.DiscoverPage),
  },
  {
    path: 'workspaces',
    canActivate: [connectionGuard],
    loadComponent: () => import('./features/workspaces/workspaces.page').then((m) => m.WorkspacesPage),
  },
  {
    path: ':ws/chats',
    canActivate: [connectionGuard],
    loadComponent: () => import('./features/chats/chats.page').then((m) => m.ChatsPage),
  },
  {
    path: ':ws/chat/:id',
    canActivate: [connectionGuard],
    loadComponent: () => import('./features/chat/chat.page').then((m) => m.ChatPage),
  },
  { path: '**', redirectTo: 'discover' },
];
