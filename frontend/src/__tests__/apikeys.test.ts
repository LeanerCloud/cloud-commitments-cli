/**
 * API Keys module tests
 */
import {
  loadApiKeys,
  renderApiKeysList,
  showCreateKeyModal,
  closeCreateKeyModal,
  createApiKey,
  handleCreateApiKey,
  showKeyCreatedModal,
  revokeApiKey,
  deleteApiKey,
  initApiKeys
} from '../apikeys';

// Mock the api module
jest.mock('../api', () => ({
  getApiKeys: jest.fn(),
  getApiKeysUsageStats: jest.fn(),
  createApiKey: jest.fn(),
  revokeApiKey: jest.fn(),
  deleteApiKey: jest.fn()
}));

// confirmDialog (introduced in P2) is used for revoke/delete in place of
// native window.confirm. Mock it so tests can flip approve/cancel via
// mockResolvedValueOnce without having to drive the actual modal UI.
const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>(() => Promise.resolve(true));
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts),
}));

import * as api from '../api';

describe('API Keys Module', () => {
  beforeEach(() => {
    // Reset DOM
    document.body.innerHTML = `
      <div id="apikeys-usage-summary"></div>
      <div id="apikeys-list"></div>
      <button id="create-apikey-btn"></button>
      <div id="create-apikey-modal" class="hidden">
        <form id="create-apikey-form">
          <input type="text" id="apikey-name" value="">
          <input type="checkbox" id="apikey-expires">
          <div id="apikey-expires-at-field" class="hidden">
            <input type="date" id="apikey-expires-at" value="">
          </div>
          <div id="create-apikey-error" class="hidden"></div>
          <button type="submit">Create</button>
        </form>
        <button id="close-create-apikey-modal-btn"></button>
      </div>
    `;

    jest.clearAllMocks();
    window.alert = jest.fn();
    window.confirm = jest.fn().mockReturnValue(true);
    // Reset the confirmDialog mock between tests — clearAllMocks only
    // clears call history, not the mockResolvedValueOnce queue.
    mockConfirmDialog.mockReset();
    mockConfirmDialog.mockImplementation(() => Promise.resolve(true));
    // Default the usage-stats endpoint to an empty response so the
    // parallel-load path in loadApiKeys doesn't reject in tests that
    // only set up `getApiKeys`. Individual tests can override.
    (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
      total_active: 0,
      total_requests_24h: 0,
      total_requests_lifetime: 0,
      top_keys: [],
    });
  });

  describe('loadApiKeys', () => {
    test('loads and renders API keys on success', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key 1',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          last_used_at: '2024-01-16T15:30:00Z'
        },
        {
          id: 'key-2',
          name: 'Test Key 2',
          key_prefix: 'xyz789',
          is_active: false,
          created_at: '2024-01-10T08:00:00Z',
          expires_at: '2024-02-10T08:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      expect(api.getApiKeys).toHaveBeenCalled();
      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Test Key 1');
      expect(container?.innerHTML).toContain('Test Key 2');
    });

    test('shows error on API failure', async () => {
      // Remove the create-apikey-error element so showError falls back to alert
      document.getElementById('create-apikey-error')?.remove();

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.getApiKeys as jest.Mock).mockRejectedValue(new Error('API Error'));

      await loadApiKeys();

      expect(consoleError).toHaveBeenCalledWith('Failed to load API keys:', expect.any(Error));
      // Q4: fallback when #create-apikey-error is absent now surfaces
      // via toast rather than blocking alert().
      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('Failed to load API keys');
      consoleError.mockRestore();
    });

    // Regression: GitHub issue #9. Backend returns `{api_keys: [...]}`
    // (auth.APIListAPIKeysResponse with `json:"api_keys"`); the previous
    // frontend read `response.keys`, leaving currentApiKeys = undefined
    // and crashing renderApiKeysList with TypeError. Lock in the
    // contract so a future field rename has to update tests too.
    test('issue #9: parses api_keys field from backend response', async () => {
      const mockKeys = [{
        id: 'key-1', name: 'Test', key_prefix: 'abc',
        is_active: true, created_at: '2024-01-15T10:00:00Z',
      }];
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      await loadApiKeys();

      expect(document.getElementById('apikeys-list')?.innerHTML).toContain('Test');
      expect(consoleError).not.toHaveBeenCalled();
      consoleError.mockRestore();
    });

    test('issue #9: tolerates legacy bare-array response without crashing', async () => {
      // Defence-in-depth — if a proxy or alternate handler returns the
      // bare array (no envelope), we should still render the rows
      // rather than crashing on currentApiKeys.length.
      const mockKeys = [{
        id: 'key-2', name: 'Bare', key_prefix: 'def',
        is_active: true, created_at: '2024-01-15T10:00:00Z',
      }];
      (api.getApiKeys as jest.Mock).mockResolvedValue(mockKeys);

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      await expect(loadApiKeys()).resolves.toBeUndefined();
      expect(document.getElementById('apikeys-list')?.innerHTML).toContain('Bare');
      expect(consoleError).not.toHaveBeenCalled();
      consoleError.mockRestore();
    });

    test('issue #9: renders empty state on unexpected response shape', async () => {
      // The exact failure mode from the bug report: response has
      // neither `api_keys` nor is an array. Old code crashed with
      // TypeError; new code should render the empty state and not log
      // an error.
      (api.getApiKeys as jest.Mock).mockResolvedValue({ unexpected: 'shape' });

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      await expect(loadApiKeys()).resolves.toBeUndefined();
      expect(document.getElementById('apikeys-list')?.textContent).toContain('No API keys yet');
      expect(consoleError).not.toHaveBeenCalled();
      consoleError.mockRestore();
    });
  });

  describe('renderApiKeysList', () => {
    test('shows empty message when no keys', () => {
      // First load empty keys
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      // Call renderApiKeysList (it uses internal state, so we need to load first)
      loadApiKeys().then(() => {
        const container = document.getElementById('apikeys-list');
        expect(container?.innerHTML).toContain('No API keys yet');
      });
    });

    test('handles missing container gracefully', () => {
      document.body.innerHTML = '';

      // Should not throw
      expect(() => renderApiKeysList()).not.toThrow();
    });

    test('renders active keys with revoke button', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Active Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('revoke-key-btn');
      expect(container?.innerHTML).toContain('Active');
    });

    test('renders revoked keys without revoke button', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Revoked Key',
          key_prefix: 'abc123',
          is_active: false,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Revoked');
      // Revoked keys should not have revoke button, but should have delete
      expect(container?.innerHTML).toContain('delete-key-btn');
    });

    test('renders expired keys with warning badge', async () => {
      const pastDate = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Expired Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          expires_at: pastDate
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Expired');
      expect(container?.innerHTML).toContain('badge-warning');
    });

    test('renders never used and never expires labels', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'New Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
          // No last_used_at or expires_at
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Never');
    });

    test('adds event listeners to revoke and delete buttons', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      (api.revokeApiKey as jest.Mock).mockResolvedValue({});

      await loadApiKeys();

      const revokeBtn = document.querySelector('.revoke-key-btn') as HTMLButtonElement;
      expect(revokeBtn).not.toBeNull();

      // Click the revoke button
      revokeBtn.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.revokeApiKey).toHaveBeenCalledWith('key-1');
    });

    // Regression for ARCH-07 (issue #1195): the previous local escapeHtml
    // copy (div.textContent -> div.innerHTML round-trip) did not escape
    // quote characters, so a key id containing a double quote interpolated
    // into data-key-id="..." broke out of the attribute and injected a new
    // attribute (here onmouseover) into the button markup.
    test('issue #1195: double quote in key id cannot inject attributes', async () => {
      const hostileId = 'key-1" onmouseover="alert(1)';
      const mockKeys = [
        {
          id: hostileId,
          name: 'Hostile Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const deleteBtn = document.querySelector('.delete-key-btn');
      expect(deleteBtn).not.toBeNull();
      expect(deleteBtn?.getAttribute('onmouseover')).toBeNull();
      expect(deleteBtn?.getAttribute('data-key-id')).toBe(hostileId);
    });

    test('adds event listener to delete buttons', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      (api.deleteApiKey as jest.Mock).mockResolvedValue({});

      await loadApiKeys();

      const deleteBtn = document.querySelector('.delete-key-btn') as HTMLButtonElement;
      expect(deleteBtn).not.toBeNull();

      // Click the delete button
      deleteBtn.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.deleteApiKey).toHaveBeenCalledWith('key-1');
    });
  });

  describe('showCreateKeyModal', () => {
    test('shows modal and resets form', () => {
      const modal = document.getElementById('create-apikey-modal');
      const nameInput = document.getElementById('apikey-name') as HTMLInputElement;
      nameInput.value = 'Previous Value';

      showCreateKeyModal();

      expect(modal?.classList.contains('hidden')).toBe(false);
      expect(nameInput.value).toBe('');
    });

    test('hides error element', () => {
      const errorEl = document.getElementById('create-apikey-error');
      errorEl?.classList.remove('hidden');

      showCreateKeyModal();

      expect(errorEl?.classList.contains('hidden')).toBe(true);
    });

    test('resets expiration checkbox and field', () => {
      const expiresCheckbox = document.getElementById('apikey-expires') as HTMLInputElement;
      const expiresAtField = document.getElementById('apikey-expires-at-field');

      expiresCheckbox.checked = true;
      expiresAtField?.classList.remove('hidden');

      showCreateKeyModal();

      expect(expiresCheckbox.checked).toBe(false);
      expect(expiresAtField?.classList.contains('hidden')).toBe(true);
    });

    test('handles missing modal gracefully', () => {
      document.body.innerHTML = '';

      // Should not throw
      expect(() => showCreateKeyModal()).not.toThrow();
    });
  });

  describe('closeCreateKeyModal', () => {
    test('hides the modal', () => {
      const modal = document.getElementById('create-apikey-modal');
      modal?.classList.remove('hidden');

      closeCreateKeyModal();

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('handles missing modal gracefully', () => {
      document.body.innerHTML = '';

      // Should not throw
      expect(() => closeCreateKeyModal()).not.toThrow();
    });
  });

  describe('createApiKey', () => {
    test('creates API key with name only', async () => {
      const mockResponse = {
        api_key: 'full-api-key-value',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'abc' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);

      const result = await createApiKey('Test Key');

      expect(api.createApiKey).toHaveBeenCalledWith({ name: 'Test Key' });
      expect(result.api_key).toBe('full-api-key-value');
    });

    test('creates API key with permissions', async () => {
      const mockResponse = {
        api_key: 'full-api-key-value',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'abc' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);

      const permissions = [{ action: 'read', resource: '*' }];
      await createApiKey('Test Key', permissions);

      expect(api.createApiKey).toHaveBeenCalledWith({
        name: 'Test Key',
        permissions: permissions
      });
    });

    test('creates API key with expiration', async () => {
      const mockResponse = {
        api_key: 'full-api-key-value',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'abc' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);

      const expiresAt = new Date('2025-12-31T00:00:00Z');
      await createApiKey('Test Key', undefined, expiresAt);

      expect(api.createApiKey).toHaveBeenCalledWith({
        name: 'Test Key',
        expires_at: expiresAt.toISOString()
      });
    });

    test('throws error on API failure', async () => {
      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.createApiKey as jest.Mock).mockRejectedValue(new Error('Create failed'));

      await expect(createApiKey('Test Key')).rejects.toThrow('Create failed');
      expect(consoleError).toHaveBeenCalled();
      consoleError.mockRestore();
    });
  });

  describe('handleCreateApiKey', () => {
    test('prevents default form submission', async () => {
      (api.createApiKey as jest.Mock).mockResolvedValue({
        api_key: 'test-key',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test', key_prefix: 'abc' }
      });
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      expect(event.preventDefault).toHaveBeenCalled();
    });

    test('shows error when name is empty', async () => {
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.classList.contains('hidden')).toBe(false);
      expect(errorEl?.textContent).toBe('API key name is required');
    });

    test('shows error when expiration date is in the past', async () => {
      const pastDate = new Date(Date.now() - 24 * 60 * 60 * 1000);

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-expires') as HTMLInputElement).checked = true;
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = pastDate.toISOString().split('T')[0] || "";

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.textContent).toBe('Expiration date must be in the future');
    });

    test('creates key and shows success modal', async () => {
      const mockResponse = {
        api_key: 'new-api-key-12345',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'new' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      // Check that the key created modal is shown
      const createdModal = document.getElementById('apikey-created-modal');
      expect(createdModal).not.toBeNull();
      expect(createdModal?.innerHTML).toContain('new-api-key-12345');
    });

    test('creates key with expiration date', async () => {
      const futureDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);

      const mockResponse = {
        api_key: 'new-api-key',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'new' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-expires') as HTMLInputElement).checked = true;
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = futureDate.toISOString().split('T')[0] || "";

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      expect(api.createApiKey).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'Test Key',
          expires_at: expect.any(String)
        })
      );
    });

    test('shows error on API failure', async () => {
      (api.createApiKey as jest.Mock).mockRejectedValue(new Error('Create failed'));

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.textContent).toContain('Failed to create API key');
    });
  });

  describe('showKeyCreatedModal', () => {
    test('creates and shows modal with API key', () => {
      showKeyCreatedModal('test-api-key-12345');

      const modal = document.getElementById('apikey-created-modal');
      expect(modal).not.toBeNull();
      expect(modal?.classList.contains('hidden')).toBe(false);
      expect(modal?.innerHTML).toContain('test-api-key-12345');
    });

    test('shows warning about one-time display', () => {
      showKeyCreatedModal('test-key');

      const modal = document.getElementById('apikey-created-modal');
      expect(modal?.innerHTML).toContain('only time');
    });

    test('removes existing modal before creating new one', () => {
      // Create first modal
      showKeyCreatedModal('first-key');

      // Create second modal
      showKeyCreatedModal('second-key');

      // Should only be one modal
      const modals = document.querySelectorAll('#apikey-created-modal');
      expect(modals.length).toBe(1);
      expect(modals[0]?.innerHTML).toContain('second-key');
    });

    test('copy button copies key to clipboard', async () => {
      const writeTextMock = jest.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true,
        configurable: true
      });

      showKeyCreatedModal('test-api-key');

      const copyBtn = document.getElementById('copy-apikey-btn');
      copyBtn?.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(writeTextMock).toHaveBeenCalledWith('test-api-key');
    });

    test('copy button shows feedback on success', async () => {
      jest.useFakeTimers();

      const writeTextMock = jest.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true,
        configurable: true
      });

      showKeyCreatedModal('test-api-key');

      const copyBtn = document.getElementById('copy-apikey-btn') as HTMLButtonElement;
      copyBtn?.click();

      await Promise.resolve();

      expect(copyBtn.textContent).toBe('Copied!');
      expect(copyBtn.classList.contains('copied')).toBe(true);

      jest.advanceTimersByTime(2000);

      expect(copyBtn.textContent).toBe('Copy');
      expect(copyBtn.classList.contains('copied')).toBe(false);

      jest.useRealTimers();
    });

    test('copy button shows alert on clipboard error', async () => {
      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      const writeTextMock = jest.fn().mockRejectedValue(new Error('Clipboard error'));
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true,
        configurable: true
      });

      showKeyCreatedModal('test-api-key');

      const copyBtn = document.getElementById('copy-apikey-btn');
      copyBtn?.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(window.alert).toHaveBeenCalledWith('Failed to copy to clipboard. Please copy manually.');
      consoleError.mockRestore();
    });

    test('close button removes modal', () => {
      showKeyCreatedModal('test-key');

      const closeBtn = document.getElementById('close-apikey-created-btn');
      closeBtn?.click();

      const modal = document.getElementById('apikey-created-modal');
      expect(modal).toBeNull();
    });
  });

  describe('revokeApiKey', () => {
    beforeEach(async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();
    });

    test('does nothing if key not found', async () => {
      await revokeApiKey('non-existent-key');

      expect(api.revokeApiKey).not.toHaveBeenCalled();
    });

    test('does nothing if user cancels confirmation', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await revokeApiKey('key-1');

      expect(api.revokeApiKey).not.toHaveBeenCalled();
    });

    test('revokes key and reloads list', async () => {
      (api.revokeApiKey as jest.Mock).mockResolvedValue({});

      await revokeApiKey('key-1');

      expect(api.revokeApiKey).toHaveBeenCalledWith('key-1');
      expect(api.getApiKeys).toHaveBeenCalledTimes(2); // Initial load + after revoke
    });

    test('shows error on API failure', async () => {
      // Remove the create-apikey-error element so showError falls back to alert
      document.getElementById('create-apikey-error')?.remove();

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.revokeApiKey as jest.Mock).mockRejectedValue(new Error('Revoke failed'));

      await revokeApiKey('key-1');

      expect(consoleError).toHaveBeenCalledWith('Failed to revoke API key:', expect.any(Error));
      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('Failed to revoke API key');
      consoleError.mockRestore();
    });
  });

  describe('deleteApiKey', () => {
    beforeEach(async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: false,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();
    });

    test('does nothing if key not found', async () => {
      await deleteApiKey('non-existent-key');

      expect(api.deleteApiKey).not.toHaveBeenCalled();
    });

    test('does nothing if user cancels confirmation', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await deleteApiKey('key-1');

      expect(api.deleteApiKey).not.toHaveBeenCalled();
    });

    test('deletes key and reloads list', async () => {
      (api.deleteApiKey as jest.Mock).mockResolvedValue({});

      await deleteApiKey('key-1');

      expect(api.deleteApiKey).toHaveBeenCalledWith('key-1');
      expect(api.getApiKeys).toHaveBeenCalledTimes(2); // Initial load + after delete
    });

    test('shows error on API failure', async () => {
      // Remove the create-apikey-error element so showError falls back to alert
      document.getElementById('create-apikey-error')?.remove();

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.deleteApiKey as jest.Mock).mockRejectedValue(new Error('Delete failed'));

      await deleteApiKey('key-1');

      expect(consoleError).toHaveBeenCalledWith('Failed to delete API key:', expect.any(Error));
      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('Failed to delete API key');
      consoleError.mockRestore();
    });
  });

  describe('initApiKeys', () => {
    test('sets up create key button', () => {
      initApiKeys();

      const createBtn = document.getElementById('create-apikey-btn');
      createBtn?.click();

      const modal = document.getElementById('create-apikey-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    test('sets up close modal button', () => {
      initApiKeys();

      // First show the modal
      const modal = document.getElementById('create-apikey-modal');
      modal?.classList.remove('hidden');

      // Click close button
      const closeBtn = document.getElementById('close-create-apikey-modal-btn');
      closeBtn?.click();

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('sets up form submission handler', async () => {
      (api.createApiKey as jest.Mock).mockResolvedValue({
        api_key: 'test-key',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test', key_prefix: 'abc' }
      });
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      initApiKeys();

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';

      const form = document.getElementById('create-apikey-form');
      form?.dispatchEvent(new Event('submit'));

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.createApiKey).toHaveBeenCalled();
    });

    test('sets up expires checkbox toggle', () => {
      initApiKeys();

      const expiresCheckbox = document.getElementById('apikey-expires') as HTMLInputElement;
      const expiresAtField = document.getElementById('apikey-expires-at-field');
      const expiresAtInput = document.getElementById('apikey-expires-at') as HTMLInputElement;

      expiresCheckbox.checked = true;
      expiresCheckbox.dispatchEvent(new Event('change'));

      expect(expiresAtField?.classList.contains('hidden')).toBe(false);
      expect(expiresAtInput.required).toBe(true);
      // Should set default date to 90 days from now
      expect(expiresAtInput.value).not.toBe('');
    });

    test('expires checkbox toggle hides field when unchecked', () => {
      initApiKeys();

      const expiresCheckbox = document.getElementById('apikey-expires') as HTMLInputElement;
      const expiresAtField = document.getElementById('apikey-expires-at-field');

      // First check, then uncheck
      expiresCheckbox.checked = true;
      expiresCheckbox.dispatchEvent(new Event('change'));

      expiresCheckbox.checked = false;
      expiresCheckbox.dispatchEvent(new Event('change'));

      expect(expiresAtField?.classList.contains('hidden')).toBe(true);
    });

    test('sets up modal backdrop click to close', () => {
      initApiKeys();

      const modal = document.getElementById('create-apikey-modal');
      modal?.classList.remove('hidden');

      // Simulate click on modal backdrop
      const clickEvent = new MouseEvent('click', { bubbles: true });
      Object.defineProperty(clickEvent, 'target', { value: modal });
      modal?.dispatchEvent(clickEvent);

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('handles missing elements gracefully', () => {
      document.body.innerHTML = '';

      // Should not throw
      expect(() => initApiKeys()).not.toThrow();
    });
  });

  describe('Error display', () => {
    test('shows error in error element when available', async () => {
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.classList.contains('hidden')).toBe(false);
      expect(errorEl?.textContent).not.toBe('');
    });

    test('falls back to toast when error element not available', async () => {
      // Remove the inline error element so showError falls back to the
      // shared toast system (Q4).
      document.getElementById('create-apikey-error')?.remove();

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('API key name is required');
    });
  });

  describe('HTML escaping', () => {
    test('escapes HTML in key names to prevent XSS', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: '<script>alert("xss")</script>',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).not.toContain('<script>');
      expect(container?.innerHTML).toContain('&lt;script&gt;');
    });

    test('escapes HTML in API key display', () => {
      showKeyCreatedModal('<script>alert("xss")</script>');

      const modal = document.getElementById('apikey-created-modal');
      expect(modal?.innerHTML).not.toContain('<script>alert');
    });
  });

  describe('Date formatting', () => {
    test('formats dates correctly in the list', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          last_used_at: '2024-01-16T15:30:00Z',
          expires_at: '2025-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      // The exact format depends on locale, but it should contain date parts
      expect(container?.innerHTML).toContain('2024');
    });

    // #492: last_used_at null renders "Never", not "0 min ago" or empty
    test('renders Never for null last_used_at (issue #492)', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Unused Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
          // last_used_at intentionally absent
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Never');
    });

    // #492: populated last_used_at renders relative time with ISO title for hover
    test('renders relative time with ISO title for populated last_used_at (issue #492)', async () => {
      const recentTs = new Date(Date.now() - 2 * 3600 * 1000).toISOString(); // 2 hours ago
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Used Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          last_used_at: recentTs
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      // Relative label: ends with "h ago" for a 2-hour-old timestamp
      expect(container?.innerHTML).toMatch(/\d+h ago/);
      // Absolute ISO on the title attribute for hover
      const span = container?.querySelector<HTMLElement>('[title]');
      expect(span?.getAttribute('title')).toBe(new Date(recentTs).toISOString());
    });
  });

  // Usage stats (issue #340/#344 deferred sub-task) — section-level summary
  // card above the table + per-row request count columns.
  describe('Usage stats', () => {
    test('renders summary tiles when stats load successfully', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });
      (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
        total_active: 2,
        total_requests_24h: 42,
        total_requests_lifetime: 1234,
        top_keys: [
          { id: 'key-1', name: 'Busy', key_prefix: 'aaaa1111', request_count_24h: 30 },
          { id: 'key-2', name: 'Medium', key_prefix: 'bbbb2222', request_count_24h: 12 },
        ],
      });

      await loadApiKeys();
      // Allow the parallel stats fetch to resolve.
      await new Promise((r) => setTimeout(r, 0));

      const summary = document.getElementById('apikeys-usage-summary');
      expect(summary?.textContent).toContain('Active keys');
      expect(summary?.textContent).toContain('2');
      expect(summary?.textContent).toContain('Requests (24h)');
      expect(summary?.textContent).toContain('42');
      expect(summary?.textContent).toContain('Requests (lifetime)');
      // formatCount abbreviates large numbers in the summary tiles to
      // fit the tile width — table cells keep the exact integer.
      expect(summary?.textContent).toContain('1.2k');
      expect(summary?.textContent).toContain('Most active (24h)');
      expect(summary?.textContent).toContain('Busy');
      expect(summary?.textContent).toContain('aaaa1111');
    });

    test('omits top-list when no key has 24h activity', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });
      (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
        total_active: 1,
        total_requests_24h: 0,
        total_requests_lifetime: 7,
        top_keys: [],
      });

      await loadApiKeys();
      await new Promise((r) => setTimeout(r, 0));

      const summary = document.getElementById('apikeys-usage-summary');
      expect(summary?.textContent).not.toContain('Most active (24h)');
    });

    test('shows inline error when stats endpoint fails, table still renders', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({
        api_keys: [
          { id: 'key-1', name: 'k', key_prefix: 'abc12345', is_active: true, created_at: '2024-01-15T10:00:00Z' },
        ],
      });
      (api.getApiKeysUsageStats as jest.Mock).mockRejectedValue(new Error('boom'));

      await loadApiKeys();
      await new Promise((r) => setTimeout(r, 0));

      const summary = document.getElementById('apikeys-usage-summary');
      expect(summary?.querySelector('.error')?.textContent).toBe('Failed to load usage summary');
      // The table itself still renders despite the summary failure.
      const list = document.getElementById('apikeys-list');
      expect(list?.innerHTML).toContain('abc12345');
    });

    test('renders Requests (24h) and Requests (total) columns per row', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({
        api_keys: [
          {
            id: 'key-1',
            name: 'k1',
            key_prefix: 'abc12345',
            is_active: true,
            created_at: '2024-01-15T10:00:00Z',
            request_count_24h: 12,
            request_count_total: 1234,
          },
        ],
      });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Requests (24h)');
      expect(container?.innerHTML).toContain('Requests (total)');
      // Counts render with thousands separator for readability.
      expect(container?.innerHTML).toContain('1,234');
      expect(container?.innerHTML).toContain('apikeys-count-cell');
    });

    test('falls back to 0 when counter fields are missing from the response', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({
        api_keys: [
          // No request_count_* fields — simulates a cached older response.
          { id: 'key-1', name: 'k1', key_prefix: 'abc12345', is_active: true, created_at: '2024-01-15T10:00:00Z' },
        ],
      });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      const cells = container?.querySelectorAll('.apikeys-count-cell');
      expect(cells?.length).toBe(2);
      expect(cells?.[0]?.textContent).toBe('0');
      expect(cells?.[1]?.textContent).toBe('0');
    });

    test('list-fetch failure clears summary skeleton and shows error', async () => {
      (api.getApiKeys as jest.Mock).mockRejectedValue(new Error('list boom'));
      (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
        total_active: 0,
        total_requests_24h: 0,
        total_requests_lifetime: 0,
        top_keys: [],
      });

      await loadApiKeys();
      await new Promise((r) => setTimeout(r, 0));

      const list = document.getElementById('apikeys-list');
      expect(list?.querySelector('.error')?.textContent).toBe('Failed to load API keys');
      // The skeleton-active marker is gone (success/error both clear it).
      expect(list?.dataset['skeletonActive']).toBeUndefined();
    });
  });
});
