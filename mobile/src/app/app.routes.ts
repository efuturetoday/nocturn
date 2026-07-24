import { Routes } from '@angular/router';
import { connectionGuard } from './core/guards/connection.guard';

/**
 * Discover sits outside the tab shell. Once connected, the tab-ROOT pages live under the `tabs`
 * shell: Home · Chat (list) · Agents · Settings. The active workspace is auto-selected (first) on
 * connect and switchable in Settings.
 *
 * Full-screen chat DETAIL (`chat/new`, `chat/:id`, `agents/run/:id`) lives OUTSIDE `tabs` — as
 * top-level siblings — so it renders in the ROOT outlet and covers the tab bar (the Ionic-native
 * way to a tab-less full-screen page; no CSS hiding). Back returns to `/tabs/chat` (the list).
 * `:id` binds to a component input via withComponentInputBinding(). `chat/new` must precede
 * `chat/:id` so it isn't captured as an id.
 */
export const routes: Routes = [
  { path: '', redirectTo: 'discover', pathMatch: 'full' },
  {
    path: 'discover',
    loadComponent: () => import('./features/discover/discover.page').then((m) => m.DiscoverPage),
  },
  {
    path: 'tabs',
    canActivate: [connectionGuard],
    loadComponent: () => import('./features/tabs/tabs.page').then((m) => m.TabsPage),
    children: [
      { path: 'home', loadComponent: () => import('./features/home/home.page').then((m) => m.HomePage) },
      { path: 'chat', loadComponent: () => import('./features/chats/chats.page').then((m) => m.ChatsPage) },
      { path: 'agents', loadComponent: () => import('./features/agents/agents.page').then((m) => m.AgentsPage) },
      { path: 'settings', loadComponent: () => import('./features/settings/settings.page').then((m) => m.SettingsPage) },
      { path: '', redirectTo: 'chat', pathMatch: 'full' },
    ],
  },
  // Full-screen (tab-less) chat detail — root-outlet siblings of `tabs`. The id is client-minted, so
  // a fresh chat navigates straight here (no /chat/new intermediate).
  // `data.kind` selects which conversation store the reused ChatPage binds to (user chats vs agent
  // runs) — the ChatPage provider reads it, so the id-addressed commands carry the right kind.
  { path: 'chat/:id', canActivate: [connectionGuard], data: { kind: 'user' }, loadComponent: () => import('./features/chat/chat.page').then((m) => m.ChatPage) },
  { path: 'agents/run/:id', canActivate: [connectionGuard], data: { kind: 'agent' }, loadComponent: () => import('./features/chat/chat.page').then((m) => m.ChatPage) },
  { path: '**', redirectTo: 'discover' },
];
