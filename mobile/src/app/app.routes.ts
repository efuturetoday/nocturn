import { Routes } from '@angular/router';
import { connectionGuard } from './core/guards/connection.guard';

/**
 * Discover sits outside the tab shell. Once connected, everything lives under the `tabs` shell:
 * Home · Chat · Agents · Settings. The active workspace is auto-selected (first) on connect and
 * switchable in Settings — there is no separate tabs-less picker screen. `chat` and `chat/:id`
 * are siblings under the Chat tab so the list→detail nav stack stays on that tab. `:id` binds to
 * a component input via withComponentInputBinding().
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
      { path: 'chat/:id', loadComponent: () => import('./features/chat/chat.page').then((m) => m.ChatPage) },
      { path: 'agents', loadComponent: () => import('./features/agents/agents.page').then((m) => m.AgentsPage) },
      { path: 'agents/run/:id', loadComponent: () => import('./features/chat/chat.page').then((m) => m.ChatPage) },
      { path: 'settings', loadComponent: () => import('./features/settings/settings.page').then((m) => m.SettingsPage) },
      { path: '', redirectTo: 'chat', pathMatch: 'full' },
    ],
  },
  { path: '**', redirectTo: 'discover' },
];
