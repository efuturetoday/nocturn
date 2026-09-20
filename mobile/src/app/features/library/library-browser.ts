import {
  Component,
  ChangeDetectionStrategy,
  inject,
  signal,
  computed,
  effect,
  input,
  linkedSignal,
} from '@angular/core';
import {
  IonContent,
  IonSearchbar,
  IonChip,
  IonLabel,
  IonList,
  IonItem,
  IonNote,
  IonButton,
  IonSpinner,
  IonModal,
  IonHeader,
  IonFooter,
  IonToolbar,
  IonTitle,
  IonButtons,
  IonRefresher,
  IonRefresherContent,
  IonGrid,
  IonRow,
  IonCol,
  AlertController,
} from '@ionic/angular/standalone';
import { LucideX } from '@lucide/angular';
import { LibraryService } from '../../core/services/library.service';
import { SkillService } from '../../core/services/skill.service';
import { PluginService } from '../../core/services/plugin.service';
import { McpService } from '../../core/services/mcp.service';
import { WorkspaceService } from '../../core/services/workspace.service';
import { MarkdownComponent } from '../../shared/markdown';
import { carries, filterCatalog, hostOf, type LibraryKind } from './library-filter';
import type { LibraryEntry } from '../../core/protocol/nocturn-protocol';

/** The filters, in the order they are offered. */
const KINDS: { key: LibraryKind; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'skill', label: 'Skills' },
  { key: 'plugin', label: 'Plugins' },
  { key: 'mcp', label: 'MCP' },
];

/**
 * The catalog as a store: search, filter chips, a grid of cards, and a detail sheet that installs.
 *
 * A component, not a page: `/app/library` frames it with the workspace header, Skills and MCP open
 * it as a dialog. It brings its own `ion-content` because the refresher needs one.
 *
 * The grid deliberately ignores `--nocturn-measure`: that measure is for columns that are read, and
 * this is a surface for picking one out of many. Column counts come from `ion-col` size-*.
 */
@Component({
  selector: 'app-library-browser',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MarkdownComponent,
    LucideX,
    IonContent,
    IonSearchbar,
    IonChip,
    IonLabel,
    IonList,
    IonItem,
    IonNote,
    IonButton,
    IonSpinner,
    IonModal,
    IonHeader,
    IonFooter,
    IonToolbar,
    IonTitle,
    IonButtons,
    IonRefresher,
    IonRefresherContent,
    IonGrid,
    IonRow,
    IonCol,
  ],
  template: `
    <ion-content>
      <ion-refresher slot="fixed" (ionRefresh)="pull($event)">
        <ion-refresher-content />
      </ion-refresher>

      @if (library.unavailable(); as why) {
        <!-- Absent, not empty. A daemon serves a catalog by default, so getting here means somebody
             switched it off or pointed it somewhere unreachable; saying "nothing found" would blame
             the catalog for a setting. -->
        <div class="state">
          <h2>No catalog</h2>
          <p>{{ why }}</p>
          <p class="dim">The daemon could not read its catalog. Pull to refresh, or check <code>NOCTURN_CATALOG_URL</code> if you set one.</p>
        </div>
      } @else if (library.loading() && !library.catalog()) {
        <div class="state"><ion-spinner name="dots" /></div>
      } @else {
        <div class="bar">
          <ion-searchbar
            placeholder="Search the catalog"
            [value]="query()"
            (ionInput)="query.set($any($event.target).value ?? '')"
          />
          <div class="filters">
            @for (k of kinds; track k.key) {
              <!-- outline stays ON in both states: toggling it changes the border and resizes the
                   chip, which shifts the whole row on every selection.

                   ion-chip renders a plain div, so the filter is spelled out as a toggle button:
                   reachable by tab, operable by Enter and Space, and announced with its pressed
                   state. The cards below are real buttons for the same reason. -->
              <ion-chip
                outline="true"
                role="button"
                tabindex="0"
                [attr.aria-pressed]="kind() === k.key"
                [class.on]="kind() === k.key"
                (click)="kind.set(k.key)"
                (keydown.enter)="kind.set(k.key)"
                (keydown.space)="$event.preventDefault(); kind.set(k.key)"
              >
                <ion-label>{{ k.label }}</ion-label>
              </ion-chip>
            }
          </div>
        </div>

        <ion-grid fixed="true">
          <ion-row>
            @for (c of entries(); track c.entry.id) {
              <ion-col size="12" size-sm="6" size-lg="4" size-xl="3">
                <button type="button" class="card" [class.have]="isInstalled(c.entry)" (click)="viewing.set(c.entry)">
                  <span class="top">
                    <span class="title">{{ c.entry.title }}</span>
                    <!-- What it CARRIES, not what kind it is: an entry may bring several. -->
                    <span class="kind">{{ c.entry.carries.join(' + ') }}</span>
                  </span>
                  <span class="desc">{{ c.entry.description }}</span>
                  @if (c.sub) {
                    <span class="sub">{{ c.sub }}</span>
                  }
                  <span class="foot">
                    @for (t of c.entry.tags ?? []; track t) {
                      <span class="tag">{{ t }}</span>
                    }
                    @if (isInstalled(c.entry)) {
                      <span class="have-mark">installed</span>
                    }
                  </span>
                </button>
              </ion-col>
            } @empty {
              <ion-col size="12">
                <p class="dim empty">{{ emptyText() }}</p>
              </ion-col>
            }
          </ion-row>
        </ion-grid>

        @if (library.version(); as v) {
          <p class="hint">Catalog {{ v }} · pull down to refresh. Installs land in {{ ws() }}.</p>
        }
      }

      <ion-modal [isOpen]="viewing() !== null" (didDismiss)="viewing.set(null)">
        <ng-template>
          <ion-header>
            <ion-toolbar>
              <ion-title>{{ viewing()?.title }}</ion-title>
              <ion-buttons slot="end">
                <ion-button (click)="viewing.set(null)" aria-label="Close">
                  <svg lucideX [size]="22" />
                </ion-button>
              </ion-buttons>
            </ion-toolbar>
          </ion-header>
          <ion-content class="detail">
            @if (viewing(); as v) {
              <p class="lead">{{ v.description }}</p>
              @if (homepage(); as home) {
                <p class="dim">{{ home }}</p>
              }

              @if ((v.settings ?? []).length) {
                <!-- What it needs before it works. Named here, because "install" that leaves a
                     second task behind is a promise this screen should not make silently. -->
                <ion-list inset="true">
                  <ion-item lines="none">
                    <ion-label class="ion-text-wrap">
                      <h3>You will need to supply</h3>
                      @for (st of v.settings ?? []; track st.name) {
                        <ion-note>{{ st.label || st.name }}@if (st.example) { — e.g. {{ st.example }} }</ion-note>
                      }
                    </ion-label>
                  </ion-item>
                </ion-list>
              }

              @if ((v.hosts ?? []).length || (v.tools ?? []).length || (v.uses ?? []).length || (v.scopes ?? []).length || v.url) {
                <!-- What installing GRANTS, before the button. The sandbox contains what code can
                     do; this table is what it ASKS for, and it is the half a person can judge. -->
                <ion-list inset="true">
                  @if (v.url) {
                    <ion-item lines="full">
                      <ion-label class="ion-text-wrap">
                        <h3>Server it dials</h3>
                        <ion-note>{{ v.url }}</ion-note>
                      </ion-label>
                    </ion-item>
                  }
                  @if ((v.tools ?? []).length) {
                    <ion-item lines="full">
                      <ion-label class="ion-text-wrap">
                        <h3>Tools it adds</h3>
                        @for (t of v.tools ?? []; track t) {
                          <ion-chip color="medium" outline="true">{{ t }}</ion-chip>
                        }
                      </ion-label>
                    </ion-item>
                  }
                  @if (carriesPlugin()) {
                    <ion-item lines="full">
                      <ion-label class="ion-text-wrap">
                        <h3>What its code may call</h3>
                        @if ((v.uses ?? []).length) {
                          @for (u of v.uses ?? []; track u) {
                            <ion-chip color="medium" outline="true">{{ u }}</ion-chip>
                          }
                        } @else {
                          <ion-note>nothing — it computes and reaches nowhere</ion-note>
                        }
                      </ion-label>
                    </ion-item>
                  }
                  @if ((v.hosts ?? []).length) {
                    <ion-item [lines]="(v.scopes ?? []).length ? 'full' : 'none'">
                      <ion-label class="ion-text-wrap">
                        <h3>A credential would ride to</h3>
                        @for (h of v.hosts ?? []; track h) {
                          <ion-chip color="medium" outline="true">{{ h }}</ion-chip>
                        }
                      </ion-label>
                    </ion-item>
                  }
                  @if ((v.scopes ?? []).length) {
                    <ion-item lines="none">
                      <ion-label class="ion-text-wrap">
                        <h3>Signing in would ask for</h3>
                        @for (sc of v.scopes ?? []; track sc) {
                          <ion-chip color="medium" outline="true">{{ sc }}</ion-chip>
                        }
                      </ion-label>
                    </ion-item>
                  }
                </ion-list>
              }

              @if (v.skill; as body) {
                <!-- The whole thing. Not truncated, not behind an accordion, no "show more" — the
                     step this screen exists for is reading it. -->
                <div class="body"><app-markdown [text]="body" /></div>
              }

              <p class="dim consent">{{ consent() }}</p>
            }
          </ion-content>

          <!-- ion-footer, not a bare toolbar. Ionic gives the home-indicator inset to the last
               toolbar INSIDE a footer; a toolbar that is merely the modal's last child gets
               nothing, so on a notched iPhone the install button sat under the indicator. -->
          <ion-footer>
            <ion-toolbar class="foot-bar">
              @if (installed()) {
                <!-- Installed is not a dead end: the one thing you want here is to take it off again. -->
                <ion-button expand="block" fill="outline" color="danger" (click)="uninstall()">
                  Uninstall from {{ ws() }}
                </ion-button>
              } @else {
                <ion-button class="install" expand="block" [disabled]="library.installing() !== null" (click)="install()">
                  @if (library.installing()) {
                    <ion-spinner name="dots" />
                  } @else {
                    Install into {{ ws() }}
                  }
                </ion-button>
              }
            </ion-toolbar>
          </ion-footer>
        </ng-template>
      </ion-modal>
    </ion-content>
  `,
  styles: `
    /* The host has to get out of the way: both callers lay out an ion-content as a flex child (a
       page's .ion-page, a modal's), and a wrapper element in between leaves the content with no
       height at all — the dialog renders its header and then nothing. */
    :host { display: contents; }

    .state { padding: var(--space-12) var(--space-6); text-align: center; }
    .state h2 { margin: 0 0 var(--space-2); font-family: var(--font-display); }
    .dim { color: var(--ion-color-medium); font-size: 0.85rem; }
    .empty { padding: var(--space-6) var(--space-2); text-align: center; }

    /* The bar rides the grid's own container so search, filters and cards share one left edge.
       Spacing is --ion-padding and its fractions, so it agrees with every Ionic component around it. */
    .bar {
      max-width: var(--ion-grid-width-xl, 1140px);
      margin-inline: auto;
      padding: var(--space-2) var(--space-1) 0;
    }
    .filters {
      display: flex;
      gap: var(--space-1);
      padding: var(--space-1) var(--space-2) var(--space-2);
    }
    /* Not color="primary": a filled Ionic chip tints its own colour at 16% and puts that colour's
       text on it, which on this background is dark purple on dark purple. Selection is a solid fill
       with contrast text — colour only, so the geometry never moves. */
    .filters ion-chip { margin: 0; --color: var(--ion-color-medium); }
    /* The real background property, not the variable: Ionic's .chip-outline sets background to
       transparent directly, so the variable alone leaves the fill off. */
    .filters ion-chip.on {
      background: var(--ion-color-primary);
      --color: var(--ion-color-primary-contrast);
      color: var(--ion-color-primary-contrast);
      border-color: var(--ion-color-primary);
    }

    /* The gutter between cards is Ionic's own column padding — set once here rather than as a margin
       on each card. */
    ion-grid {
      --ion-grid-column-padding: var(--space-2);
      padding: 0 var(--space-2) var(--space-4);
    }

    /* Same card language as the discover screen: one lift off the background, one hairline. */
    .card {
      display: flex; flex-direction: column; gap: var(--space-2);
      width: 100%; height: 100%; padding: var(--space-4);
      background: var(--ion-background-color-step-100); color: var(--ion-text-color);
      border: 1px solid var(--ion-background-color-step-150); border-radius: 0.875rem;
      font: inherit; text-align: left; cursor: pointer;
    }
    .card:hover { border-color: var(--ion-color-primary); }
    .card .top { display: flex; align-items: baseline; gap: var(--space-2); }
    .card .title { flex: 1; font-weight: 600; }
    .card .kind { color: var(--ion-color-medium); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; }
    /* Two lines, so a long description cannot make one card twice the height of its neighbours. */
    .card .desc {
      display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden;
      color: var(--ion-color-medium); font-size: 0.85rem;
    }
    .card .sub { color: var(--ion-color-medium); font-size: 0.75rem; word-break: break-all; }
    .card .foot {
      display: flex; flex-wrap: wrap; align-items: center;
      gap: var(--space-1);
      margin-top: auto; padding-top: var(--space-1);
    }
    .card .tag {
      display: inline-flex; align-items: center; height: 1.25rem; line-height: 1;
      padding: 0 var(--space-2);
      border: 1px solid var(--ion-background-color-step-200); border-radius: 999px;
      color: var(--ion-color-medium); font-size: 0.7rem;
    }
    /* An installed entry keeps its place — the store is also how you check whether you have one. */
    .card.have { opacity: 0.72; }
    .card .have-mark { margin-left: auto; color: var(--ion-color-success); font-size: 0.7rem; }

    .detail {
      --padding-start: var(--space-4);
      --padding-end: var(--space-4);
      --padding-top: var(--space-2);
    }
    .detail .lead { margin: 0 0 var(--space-1); }
    .detail .body { margin-top: var(--space-2); }
    .detail .consent { margin-top: var(--space-4); }
    .foot-bar {
      --padding-start: var(--space-2);
      --padding-end: var(--space-2);
    }
    /* Stated, not inherited: inside a toolbar the button picks up the toolbar's text colour and
       comes out dark on purple. */
    .install {
      --background: var(--ion-color-primary);
      --color: #fff;
      --color-hover: #fff;
      --color-focused: #fff;
      --color-activated: #fff;
    }
    /* The shadow part, because the variables above lose to a rule inside the button that reads the
       toolbar's own text colour. */
    .install::part(native) { color: #fff; }
    .hint {
      max-width: var(--ion-grid-width-xl, 1140px);
      margin-inline: auto;
      margin-block: 0;
      padding: 0 var(--space-4) var(--space-4);
      color: var(--ion-color-medium);
      font-size: 0.85rem;
    }
  `,
})
export class LibraryBrowserComponent {
  protected readonly library = inject(LibraryService);
  private readonly skillsSvc = inject(SkillService);
  private readonly pluginsSvc = inject(PluginService);
  private readonly mcpSvc = inject(McpService);
  private readonly workspaces = inject(WorkspaceService);
  private readonly alerts = inject(AlertController);

  /** Which filter to land on. The MCP page opens the store because it wants a server, so starting on
      `all` would make it search past its own answer. */
  readonly initial = input<LibraryKind>('all');

  protected readonly kinds = KINDS;
  protected readonly query = signal('');
  protected readonly kind = linkedSignal(() => this.initial());
  protected readonly viewing = signal<LibraryEntry | null>(null);

  /** The id of an install this screen started, so the sheet knows which arrival is its own. */
  private readonly pending = signal<string | null>(null);

  protected readonly entries = computed(() => filterCatalog(this.library.catalog(), this.query(), this.kind()));

  /** Where an install lands. Named on the button: the catalog is daemon-wide, the target is not. */
  protected readonly ws = computed(() => this.workspaces.activeTitle());

  protected readonly homepage = computed(() => this.viewing()?.homepage ?? '');
  protected readonly carriesPlugin = computed(() => {
    const v = this.viewing();
    return v !== null && carries(v, 'plugin');
  });

  /**
   * The sentence under the table, chosen by what the entry actually brings.
   *
   * Three different promises, and saying the wrong one is worse than saying nothing: text grants
   * nothing, code runs sandboxed, a server is dialled only after somebody signs in.
   */
  protected readonly consent = computed(() => {
    const v = this.viewing();
    if (!v) return '';
    const parts: string[] = [];
    if (carries(v, 'skill')) {
      parts.push(
        'The instructions above are what the assistant will be told. They grant nothing on their own — ' +
          'anything they ask for still meets the gate.',
      );
    }
    if (carries(v, 'plugin')) {
      parts.push('Its code runs in the sandbox — no ambient authority, and every effect still meets the gate.');
    }
    if (carries(v, 'mcp')) {
      parts.push("Signing in happens later, from the server's row on the MCP page.");
    }
    if ((v.settings ?? []).length) {
      parts.push('Installing writes the folder; the settings above are supplied afterwards on the host.');
    }
    return parts.join(' ');
  });

  constructor() {
    // Asked for here rather than on connect: a daemon without a catalog answers with an error, which
    // would otherwise toast at startup for a page nobody opened.
    this.library.list();

    // Close the sheet once the install WE started shows up in the target list. Keyed on that id
    // rather than on "is installed", which would slam the sheet shut the moment you opened
    // something you already have.
    effect(() => {
      const id = this.pending();
      const v = this.viewing();
      if (id && v && v.id === id && this.isInstalled(v)) {
        this.pending.set(null);
        this.viewing.set(null);
      }
    });
  }

  protected emptyText(): string {
    return this.query().trim() ? 'Nothing matches that.' : 'The catalog offers none of these yet.';
  }

  /**
   * Whether this entry is already installed, asked of whichever domain can answer.
   *
   * The entry's id IS its folder, so a server and a plugin are matched by it directly. A skill is
   * the exception the daemon itself makes: it is filed under its frontmatter name, which need not be
   * the catalog id.
   */
  protected isInstalled(e: LibraryEntry): boolean {
    if (carries(e, 'mcp') && this.mcpSvc.servers().some((x) => x.name === e.id)) return true;
    if (carries(e, 'plugin') && this.pluginsSvc.plugins().some((x) => x.name === e.id)) return true;
    if (carries(e, 'skill')) {
      const name = frontmatterName(e.skill ?? '') ?? e.id;
      return this.skillsSvc.skills().some((x) => x.name === name);
    }
    return false;
  }

  protected installed(): boolean {
    const v = this.viewing();
    return v !== null && this.isInstalled(v);
  }

  protected install(): void {
    const v = this.viewing();
    if (!v) return;
    this.pending.set(v.id);
    this.library.install(v.id);
  }

  /**
   * Remove what this entry installed.
   *
   * One folder, one removal — that is the point of one tree. What used to be three cases (a skill's
   * folder, a plugin's folder, a server's declaration) is one, and the standing permission its
   * credential's host holds goes with it, which is the part a person needs told BEFORE agreeing.
   */
  protected async uninstall(): Promise<void> {
    const v = this.viewing();
    if (!v) return;

    if (carries(v, 'plugin')) {
      // Not a button yet: removing code has to revoke the remembered permission for the hosts its
      // credential rode to, and half of that would leave a grant standing for a program that is gone.
      const alert = await this.alerts.create({
        header: `Remove ${v.id}?`,
        message:
          `Not from here yet. On the machine running Nocturn, delete the folder ` +
          `extensions/${v.id} and run \`nocturn reload\`.`,
        buttons: [{ text: 'OK', role: 'cancel' }],
      });
      await alert.present();
      return;
    }

    if (carries(v, 'mcp')) {
      const where = v.url ? hostOf(v.url) : v.id;
      const alert = await this.alerts.create({
        header: `Remove ${v.id}?`,
        message:
          `Its declaration and any token stored for it are deleted, and the remembered permission to ` +
          `reach ${where} is revoked — if you had allowed that host for something else, Nocturn will ` +
          `ask about it once more.`,
        buttons: [
          { text: 'Cancel', role: 'cancel' },
          { text: 'Remove', role: 'destructive', handler: () => this.mcpSvc.remove(v.id) },
        ],
      });
      await alert.present();
      return;
    }

    const name = frontmatterName(v.skill ?? '') ?? v.id;
    const alert = await this.alerts.create({
      header: `Delete ${name}?`,
      message:
        `Its folder is deleted — there is no trash for skills. You can install it again from here; ` +
        `to keep it without the assistant seeing it, switch it off on the Skills tab instead.`,
      buttons: [
        { text: 'Cancel', role: 'cancel' },
        { text: 'Delete', role: 'destructive', handler: () => this.skillsSvc.remove(name) },
      ],
    });
    await alert.present();
  }

  protected pull(ev: Event): void {
    this.library.refresh();
    void (ev as CustomEvent & { target: { complete(): Promise<void> } }).target.complete();
  }
}

/** The `name:` from a SKILL.md preamble — the identity the daemon files a skill under, which the
    catalog id need not match. Null when there is no preamble to read it from. */
function frontmatterName(body: string): string | null {
  const end = body.startsWith('---\n') ? body.indexOf('\n---', 4) : -1;
  if (end < 0) return null;
  const line = body
    .slice(4, end)
    .split('\n')
    .find((l) => l.startsWith('name:'));
  return line
    ? line
        .slice(5)
        .trim()
        .replace(/^["']|["']$/g, '') || null
    : null;
}
