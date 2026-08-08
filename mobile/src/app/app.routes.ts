import { Routes } from '@angular/router';
import { connectionGuard } from './core/guards/connection.guard';

/**
 * Discover sits outside the app shell. Everything else lives under `app`, inside the ShellPage's
 * ion-router-outlet, so the side menu is reachable from every screen — including from inside a
 * chat, which is what makes the drawer's history the way back.
 *
 * Chat DETAIL is an ordinary shell child. It used to be a root-outlet sibling purely so it would
 * cover the bottom tab bar; with the tab bar gone there is nothing to cover, and being a child is
 * what gives it the menu.
 *
 * `:id` binds to a component input via withComponentInputBinding(). `data.kind` selects which
 * conversation store the reused ChatPage binds to (user chats vs agent runs) — the ChatPage
 * provider reads it, so the id-addressed commands carry the right kind.
 */
export const routes: Routes = [
  { path: '', redirectTo: 'discover', pathMatch: 'full' },
  {
    path: 'discover',
    loadComponent: () => import('./features/discover/discover.page').then((m) => m.DiscoverPage),
  },
  {
    path: 'app',
    canActivate: [connectionGuard],
    loadComponent: () => import('./features/shell/shell.page').then((m) => m.ShellPage),
    children: [
      { path: 'home', loadComponent: () => import('./features/home/home.page').then((m) => m.HomePage) },
      { path: 'reminders', loadComponent: () => import('./features/reminders/reminders.page').then((m) => m.RemindersPage) },
      { path: 'agents', loadComponent: () => import('./features/agents/agents.page').then((m) => m.AgentsPage) },
      { path: 'settings', loadComponent: () => import('./features/settings/settings.page').then((m) => m.SettingsPage) },
      // The chat id is client-minted, so a fresh chat navigates straight here (no /chat/new step).
      { path: 'chat/:id', data: { kind: 'user' }, loadComponent: () => import('./features/chat/chat.page').then((m) => m.ChatPage) },
      { path: 'agents/run/:id', data: { kind: 'agent' }, loadComponent: () => import('./features/chat/chat.page').then((m) => m.ChatPage) },
      { path: '', redirectTo: 'home', pathMatch: 'full' },
    ],
  },
  { path: '**', redirectTo: 'discover' },
];
