import { Component, ChangeDetectionStrategy } from '@angular/core';
import {
  IonToolbar, IonSegment, IonSegmentButton, IonSegmentView, IonSegmentContent, IonLabel,
} from '@ionic/angular/standalone';
import { WorkspaceHeaderComponent } from '../../shared/workspace-header';
import { SettingsGeneralPage } from './settings-general.page';
import { SettingsDevicesPage } from './settings-devices.page';
import { SkillsPage } from '../skills/skills.page';
import { PluginsPage } from '../plugins/plugins.page';
import { McpPage } from '../mcp/mcp.page';
import { WorkspacesPage } from '../workspaces/workspaces.page';

/**
 * Settings holds what a household configures, as tabs.
 *
 * Tabs rather than five drawer rows: the drawer's lower half is the chat list, and every row above
 * it pushes the chats down. The bar only exists on the screen that needs it.
 *
 * ion-segment-view rather than a child outlet: it swipes and it animates in the direction of travel
 * on its own. The price is that all five panes live in one URL and are in the DOM together — which
 * is cheap here, because each pane's data comes from a root service that lists on connect anyway.
 */
@Component({
  selector: 'app-settings',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    WorkspaceHeaderComponent,
    SettingsGeneralPage, SettingsDevicesPage, SkillsPage, PluginsPage, McpPage, WorkspacesPage,
    IonToolbar, IonSegment, IonSegmentButton, IonSegmentView, IonSegmentContent, IonLabel,
  ],
  template: `
    <app-workspace-header />

    <ion-toolbar class="tabs">
      <!-- scrollable: five labels do not fit a phone, and Ionic scrolls the bar rather than
           squeezing them. No breakpoint involved. -->
      <ion-segment scrollable="true" value="general">
        <ion-segment-button value="general" contentId="general"><ion-label>General</ion-label></ion-segment-button>
        <ion-segment-button value="devices" contentId="devices"><ion-label>Devices</ion-label></ion-segment-button>
        <ion-segment-button value="skills" contentId="skills"><ion-label>Skills</ion-label></ion-segment-button>
        <ion-segment-button value="plugins" contentId="plugins"><ion-label>Plugins</ion-label></ion-segment-button>
        <ion-segment-button value="mcp" contentId="mcp"><ion-label>MCP</ion-label></ion-segment-button>
        <ion-segment-button value="workspaces" contentId="workspaces"><ion-label>Workspaces</ion-label></ion-segment-button>
      </ion-segment>
    </ion-toolbar>

    <ion-segment-view>
      <ion-segment-content id="general"><app-settings-general /></ion-segment-content>
      <ion-segment-content id="devices"><app-settings-devices /></ion-segment-content>
      <ion-segment-content id="skills"><app-skills /></ion-segment-content>
      <ion-segment-content id="plugins"><app-plugins /></ion-segment-content>
      <ion-segment-content id="mcp"><app-mcp /></ion-segment-content>
      <ion-segment-content id="workspaces"><app-workspaces /></ion-segment-content>
    </ion-segment-view>
  `,
  styles: `
    .tabs { --background: transparent; --min-height: 0; }
    ion-segment-view { flex: 1 1 auto; min-height: 0; }
    /* Each pane holds a page whose root is an ion-content, and an ion-content needs a parent with a
       height to scroll inside. */
    ion-segment-content > * {
      display: flex;
      flex-direction: column;
      height: 100%;
    }
  `,
})
export class SettingsPage {}
