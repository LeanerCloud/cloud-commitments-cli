/**
 * API Keys Management module for CUDly
 */

import * as api from './api';
import type { APIKeyInfo, CreateAPIKeyResponse } from './types';
import type { APIKeysUsageStats } from './api/types';
import { escapeHtml, formatDateTime, formatRelativeTime } from './utils';
import { confirmDialog } from './confirmDialog';
import { showToast } from './toast';
import { openModal, closeModal } from './modal';
import { showSkeletonRows, showSkeletonBlock, teardownSkeleton } from './lib/skeleton';

// State for modal management
let currentApiKeys: APIKeyInfo[] = [];

/**
 * Load and display API keys.
 *
 * Defensive parsing (issue #9): the previous implementation read
 * `response.keys` while the backend returns `{api_keys: [...]}`,
 * leaving `currentApiKeys` set to undefined and crashing the next
 * render with "Cannot read properties of undefined (reading 'length')".
 * Read the documented `api_keys` field, then fall back to a bare array
 * (some other deployments/proxies might unwrap) and finally to `[]` so
 * a contract drift can never crash the page.
 *
 * The usage-stats summary loads in parallel -- a failure in either path
 * only takes down its own region, never both. See loadApiKeysUsageStats
 * for the section header's lifecycle.
 */
export async function loadApiKeys(): Promise<void> {
  const listContainer = document.getElementById('apikeys-list');
  if (listContainer) {
    showSkeletonRows(listContainer, 3, 9);
  }
  // Kick the summary off in parallel -- it has its own container + error
  // path so we don't await it inside the list flow.
  void loadApiKeysUsageStats();

  try {
    const response = await api.getApiKeys();
    const list = (response as { api_keys?: APIKeyInfo[] } | undefined)?.api_keys
      ?? (Array.isArray(response) ? response as APIKeyInfo[] : undefined)
      ?? [];
    currentApiKeys = Array.isArray(list) ? list : [];
    renderApiKeysList();
  } catch (error) {
    console.error('Failed to load API keys:', error);
    if (listContainer) teardownSkeleton(listContainer);
    renderApiKeysListError(listContainer, 'Failed to load API keys');
    showError('Failed to load API keys');
  }
}

/**
 * Replace the API keys list region with an inline error message so the
 * shimmer skeleton doesn't sit beside a stale empty table after a
 * failed fetch. Uses textContent (no innerHTML) to stay XSS-safe and
 * matches the patterns used by other modules (e.g. dashboard.ts).
 */
function renderApiKeysListError(container: HTMLElement | null, message: string): void {
  if (!container) return;
  container.replaceChildren();
  const p = document.createElement('p');
  p.className = 'error';
  p.textContent = message;
  container.appendChild(p);
}

/**
 * Load and render the section-level usage summary (totals + top keys).
 * Fails closed: on error the summary slot shows an inline message and
 * the rest of the section still works.
 */
export async function loadApiKeysUsageStats(): Promise<void> {
  const container = document.getElementById('apikeys-usage-summary');
  if (!container) return;
  showSkeletonBlock(container, '100%', '4rem');
  try {
    const stats = await api.getApiKeysUsageStats();
    renderApiKeysUsageSummary(stats);
  } catch (error) {
    console.error('Failed to load API keys usage stats:', error);
    teardownSkeleton(container);
    const p = document.createElement('p');
    p.className = 'error';
    p.textContent = 'Failed to load usage summary';
    container.appendChild(p);
  }
}

/**
 * Render the section-level summary card: total active keys, total
 * requests (24h + lifetime), and a top-3 most-active row. Built with
 * createElement only (no innerHTML) to match the codebase XSS posture.
 */
export function renderApiKeysUsageSummary(stats: APIKeysUsageStats): void {
  const container = document.getElementById('apikeys-usage-summary');
  if (!container) return;
  container.replaceChildren();
  delete container.dataset['skeletonActive'];

  const card = document.createElement('div');
  card.className = 'apikeys-usage-summary card';

  const tiles = document.createElement('div');
  tiles.className = 'apikeys-usage-tiles';
  tiles.appendChild(buildSummaryTile('Active keys', String(stats.total_active)));
  tiles.appendChild(buildSummaryTile('Requests (24h)', formatCount(stats.total_requests_24h)));
  tiles.appendChild(buildSummaryTile('Requests (lifetime)', formatCount(stats.total_requests_lifetime)));
  card.appendChild(tiles);

  if (stats.top_keys && stats.top_keys.length > 0) {
    const heading = document.createElement('h5');
    heading.className = 'apikeys-usage-top-heading';
    heading.textContent = 'Most active (24h)';
    card.appendChild(heading);

    const list = document.createElement('ul');
    list.className = 'apikeys-usage-top-list';
    for (const top of stats.top_keys) {
      const li = document.createElement('li');
      const name = document.createElement('strong');
      name.textContent = top.name;
      const code = document.createElement('code');
      code.textContent = `${top.key_prefix}...`;
      const count = document.createElement('span');
      count.className = 'apikeys-usage-top-count';
      count.textContent = `${formatCount(top.request_count_24h)} req`;
      li.appendChild(name);
      li.appendChild(document.createTextNode(' '));
      li.appendChild(code);
      li.appendChild(document.createTextNode(' — '));
      li.appendChild(count);
      list.appendChild(li);
    }
    card.appendChild(list);
  }

  container.appendChild(card);
}

function buildSummaryTile(label: string, value: string): HTMLElement {
  const tile = document.createElement('div');
  tile.className = 'apikeys-usage-tile';
  const labelEl = document.createElement('div');
  labelEl.className = 'apikeys-usage-tile-label';
  labelEl.textContent = label;
  const valueEl = document.createElement('div');
  valueEl.className = 'apikeys-usage-tile-value';
  valueEl.textContent = value;
  tile.appendChild(labelEl);
  tile.appendChild(valueEl);
  return tile;
}

/**
 * Format a request count for display in the summary tiles. Uses the
 * standard "k" / "M" abbreviation for large values so the tile doesn't
 * have to fit "1,234,567" -- the per-row table cells still show the
 * exact number via formatRequestCount.
 */
function formatCount(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '0';
  if (n < 1000) return String(Math.trunc(n));
  if (n < 1_000_000) {
    // Round to the chosen precision FIRST so we can detect the
    // 999,500..999,999 band that rounds up to 1000k and promote
    // it to the M branch instead of emitting "1000k".
    const k = Number((n / 1000).toFixed(n < 10_000 ? 1 : 0));
    if (k < 1000) return `${k}k`;
  }
  return `${(n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0)}M`;
}

/**
 * Render API keys list
 */
export function renderApiKeysList(): void {
  const container = document.getElementById('apikeys-list');
  if (!container) return;

  if (!Array.isArray(currentApiKeys) || currentApiKeys.length === 0) {
    // DOM construction rather than template literal so the security hook
    // doesn't flag the innerHTML write — and all copy is static anyway.
    container.replaceChildren();
    delete container.dataset['skeletonActive'];
    const wrap = document.createElement('div');
    wrap.className = 'empty apikeys-empty';
    const h = document.createElement('h4');
    h.textContent = 'No API keys yet';
    const p = document.createElement('p');
    p.textContent = 'Create an API key to let automation tools (CI pipelines, scripts, integrations) call CUDly programmatically. Each key can be revoked or rotated at any time.';
    wrap.appendChild(h);
    wrap.appendChild(p);
    container.appendChild(wrap);
    return;
  }

  const table = `
    <table class="data-table">
      <thead>
        <tr>
          <th>Name</th>
          <th>Key Prefix</th>
          <th>Status</th>
          <th>Created</th>
          <th>Last Used</th>
          <th>Requests (24h)</th>
          <th>Requests (total)</th>
          <th>Expires</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${currentApiKeys.map(key => {
          const isExpired = key.expires_at && new Date(key.expires_at) < new Date();
          const statusClass = !key.is_active ? 'badge-danger' : isExpired ? 'badge-warning' : 'badge-success';
          const statusText = !key.is_active ? 'Revoked' : isExpired ? 'Expired' : 'Active';
          const count24h = formatRequestCount(key.request_count_24h);
          const countTotal = formatRequestCount(key.request_count_total);

          return `
            <tr>
              <td><strong>${escapeHtml(key.name)}</strong></td>
              <td><code>${escapeHtml(key.key_prefix)}...</code></td>
              <td><span class="badge ${statusClass}">${statusText}</span></td>
              <td>${formatDateTime(key.created_at)}</td>
              <td>${key.last_used_at ? `<span title="${escapeHtml(new Date(key.last_used_at).toISOString())}">${escapeHtml(formatRelativeTime(key.last_used_at))}</span>` : '<span class="text-muted">Never</span>'}</td>
              <td class="apikeys-count-cell">${count24h}</td>
              <td class="apikeys-count-cell">${countTotal}</td>
              <td>${key.expires_at ? formatDateTime(key.expires_at) : '<span class="text-muted">Never</span>'}</td>
              <td>
                ${key.is_active && !isExpired ? `<button class="btn-small btn-warning revoke-key-btn" data-key-id="${escapeHtml(key.id)}">Revoke</button>` : ''}
                <button class="btn-small btn-danger delete-key-btn" data-key-id="${escapeHtml(key.id)}">Delete</button>
              </td>
            </tr>
          `;
        }).join('')}
      </tbody>
    </table>
  `;

  // Clear the skeleton marker before the render — the existing
  // innerHTML write (kept intact for diff minimality) replaces all
  // children, and we don't want a stale `data-skeleton-active`
  // attribute lingering on the live table.
  delete container.dataset['skeletonActive'];
  container.innerHTML = table;

  // Add event delegation after rendering
  container.querySelectorAll('.revoke-key-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const keyId = (btn as HTMLElement).dataset.keyId;
      if (keyId) void revokeApiKey(keyId);
    });
  });
  container.querySelectorAll('.delete-key-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const keyId = (btn as HTMLElement).dataset.keyId;
      if (keyId) void deleteApiKey(keyId);
    });
  });
}

/**
 * Show create API key modal
 */
export function showCreateKeyModal(): void {
  const modal = document.getElementById('create-apikey-modal');
  const form = document.getElementById('create-apikey-form') as HTMLFormElement;
  const errorEl = document.getElementById('create-apikey-error');

  if (!modal || !form) return;

  form.reset();
  if (errorEl) errorEl.classList.add('hidden');

  // Reset expiration checkbox and field visibility
  const expiresCheckbox = document.getElementById('apikey-expires') as HTMLInputElement;
  const expiresAtField = document.getElementById('apikey-expires-at-field');
  if (expiresCheckbox) expiresCheckbox.checked = false;
  if (expiresAtField) expiresAtField.classList.add('hidden');

  openModal(modal);
}

/**
 * Close create API key modal
 */
export function closeCreateKeyModal(): void {
  const modal = document.getElementById('create-apikey-modal');
  if (modal) closeModal(modal);
}

/**
 * Create new API key
 */
export async function createApiKey(name: string, permissions?: api.Permission[], expiresAt?: Date): Promise<CreateAPIKeyResponse> {
  try {
    const request: api.CreateAPIKeyRequest = { name };

    if (permissions && permissions.length > 0) {
      request.permissions = permissions;
    }

    if (expiresAt) {
      request.expires_at = expiresAt.toISOString();
    }

    const response = await api.createApiKey(request);
    return response;
  } catch (error) {
    console.error('Failed to create API key:', error);
    throw error;
  }
}

/**
 * Handle create API key form submission
 */
export async function handleCreateApiKey(e: Event): Promise<void> {
  e.preventDefault();

  const errorEl = document.getElementById('create-apikey-error');
  if (errorEl) errorEl.classList.add('hidden');

  const name = (document.getElementById('apikey-name') as HTMLInputElement | null)?.value.trim() ?? '';
  const expiresCheckbox = (document.getElementById('apikey-expires') as HTMLInputElement | null)?.checked ?? false;
  const expiresAtInput = (document.getElementById('apikey-expires-at') as HTMLInputElement | null)?.value ?? '';

  if (!name) {
    showError('API key name is required');
    return;
  }

  let expiresAt: Date | undefined;
  if (expiresCheckbox && expiresAtInput) {
    expiresAt = new Date(expiresAtInput);
    if (expiresAt <= new Date()) {
      showError('Expiration date must be in the future');
      return;
    }
  }

  try {
    const response = await createApiKey(name, undefined, expiresAt);
    closeCreateKeyModal();
    showKeyCreatedModal(response.api_key);
    await loadApiKeys();
  } catch (error) {
    const err = error as Error;
    showError(`Failed to create API key: ${err.message}`);
  }
}

/**
 * Show key created modal with one-time display
 */
export function showKeyCreatedModal(apiKey: string): void {
  // Remove any existing modal to prevent duplicates
  document.getElementById('apikey-created-modal')?.remove();

  const modal = document.createElement('div');
  modal.id = 'apikey-created-modal';
  modal.className = 'modal';
  modal.innerHTML = `
    <div class="modal-content">
      <h2>API Key Created Successfully</h2>
      <div class="warning-box">
        <strong>Important:</strong> This is the only time you'll see this API key.
        Please copy it now and store it securely.
      </div>
      <div class="apikey-display">
        <label>Your API Key:</label>
        <div class="apikey-value-container">
          <code id="apikey-value" class="apikey-value">${escapeHtml(apiKey)}</code>
          <button type="button" id="copy-apikey-btn" class="btn-small primary">Copy</button>
        </div>
      </div>
      <div class="modal-info">
        <p>Use this key in the <code>X-API-Key</code> header when making API requests:</p>
        <pre class="code-example">curl -H "X-API-Key: ${escapeHtml(apiKey)}" https://your-api-endpoint.com/api/...</pre>
      </div>
      <div class="modal-buttons">
        <button type="button" id="close-apikey-created-btn" class="primary">I've Copied the Key</button>
      </div>
    </div>
  `;

  document.body.appendChild(modal);
  openModal(modal);

  // Setup copy button
  const copyBtn = document.getElementById('copy-apikey-btn');
  if (copyBtn) {
    copyBtn.addEventListener('click', () => {
      navigator.clipboard.writeText(apiKey).then(() => {
        copyBtn.textContent = 'Copied!';
        copyBtn.classList.add('copied');
        setTimeout(() => {
          copyBtn.textContent = 'Copy';
          copyBtn.classList.remove('copied');
        }, 2000);
      }).catch(err => {
        console.error('Failed to copy:', err);
        alert('Failed to copy to clipboard. Please copy manually.');
      });
    });
  }

  // Setup close button. closeModal first so the focus trap is torn
  // down and focus is restored to the original trigger; then remove
  // the dynamically-injected element from the DOM.
  const closeBtn = document.getElementById('close-apikey-created-btn');
  if (closeBtn) {
    closeBtn.addEventListener('click', () => {
      closeModal(modal);
      modal.remove();
    });
  }
}

/**
 * Revoke an API key
 */
export async function revokeApiKey(keyId: string): Promise<void> {
  const key = currentApiKeys.find(k => k.id === keyId);
  if (!key) return;

  const ok = await confirmDialog({
    title: `Revoke API key "${key.name}"?`,
    body: 'The key will immediately stop working. This action cannot be undone. (You can delete the row afterwards to remove it from the list.)',
    confirmLabel: 'Revoke key',
    destructive: true,
  });
  if (!ok) return;

  try {
    await api.revokeApiKey(keyId);
    await loadApiKeys();
  } catch (error) {
    console.error('Failed to revoke API key:', error);
    showError('Failed to revoke API key');
  }
}

/**
 * Delete an API key
 */
export async function deleteApiKey(keyId: string): Promise<void> {
  const key = currentApiKeys.find(k => k.id === keyId);
  if (!key) return;

  const ok = await confirmDialog({
    title: `Delete API key "${key.name}"?`,
    body: 'This permanently removes the key from the list. If the key is still active, it will also stop working.',
    confirmLabel: 'Delete key',
    destructive: true,
  });
  if (!ok) return;

  try {
    await api.deleteApiKey(keyId);
    await loadApiKeys();
  } catch (error) {
    console.error('Failed to delete API key:', error);
    showError('Failed to delete API key');
  }
}

/**
 * Initialize API keys management
 */
export function initApiKeys(): void {
  // Setup create key button
  const createKeyBtn = document.getElementById('create-apikey-btn');
  if (createKeyBtn) {
    createKeyBtn.addEventListener('click', () => showCreateKeyModal());
  }

  // Setup close modal button
  const closeModalBtn = document.getElementById('close-create-apikey-modal-btn');
  if (closeModalBtn) {
    closeModalBtn.addEventListener('click', () => closeCreateKeyModal());
  }

  // Setup form submission
  const form = document.getElementById('create-apikey-form');
  if (form) {
    form.addEventListener('submit', (e) => void handleCreateApiKey(e));
  }

  // Setup expires checkbox toggle
  const expiresCheckbox = document.getElementById('apikey-expires') as HTMLInputElement;
  const expiresAtField = document.getElementById('apikey-expires-at-field');
  if (expiresCheckbox && expiresAtField) {
    expiresCheckbox.addEventListener('change', () => {
      expiresAtField.classList.toggle('hidden', !expiresCheckbox.checked);
      const expiresAtInput = document.getElementById('apikey-expires-at') as HTMLInputElement;
      if (expiresAtInput) {
        expiresAtInput.required = expiresCheckbox.checked;
        // Set default to 90 days from now
        if (expiresCheckbox.checked && !expiresAtInput.value) {
          const defaultDate = new Date();
          defaultDate.setDate(defaultDate.getDate() + 90);
          expiresAtInput.value = defaultDate.toISOString().split('T')[0] || "";
        }
      }
    });
  }

  // Close modal when clicking outside
  const modal = document.getElementById('create-apikey-modal');
  if (modal) {
    modal.addEventListener('click', (e) => {
      if (e.target === modal) closeCreateKeyModal();
    });
  }
}

/**
 * Show error message.
 *
 * When the Create-API-Key modal is open, validation-style errors belong
 * inline on the form (so the user sees them next to the offending
 * field). Outside that context — load failures, revoke/delete errors —
 * we surface via the shared toast system (Q4) so the message matches
 * the rest of the app rather than using a blocking alert().
 */
function showError(message: string): void {
  const errorEl = document.getElementById('create-apikey-error');
  if (errorEl) {
    errorEl.textContent = message;
    errorEl.classList.remove('hidden');
  } else {
    showToast({ message, kind: 'error' });
  }
}

/**
 * Format a per-row request count for display in the table cells.
 * Renders the exact integer (no abbreviation) so a row with "1,234,567"
 * is unambiguous — the section-level summary card uses a separate
 * `formatCount` that abbreviates large values to fit the tile width.
 *
 * Defends against missing fields (older cached responses without the
 * counters from migration 000094) and non-finite inputs by coercing
 * to 0 — never returns "undefined" or "NaN" to the rendered table.
 */
function formatRequestCount(n: number | undefined): string {
  if (typeof n !== 'number' || !Number.isFinite(n) || n < 0) return '0';
  return Math.trunc(n).toLocaleString('en-US');
}
