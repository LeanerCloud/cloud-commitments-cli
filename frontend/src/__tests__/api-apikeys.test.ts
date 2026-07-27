/**
 * Tests for src/api/apikeys.ts module
 * These tests use the fetchMock directly to test the actual API functions
 */
import { fetchMock } from './setup';
import {
  getApiKeys,
  getApiKeysUsageStats,
  createApiKey,
  revokeApiKey,
  deleteApiKey
} from '../api/apikeys';
import { clearAuth, setAuthToken } from '../api/client';

describe('API Keys API Module', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    clearAuth();
    setAuthToken('test-token');
  });

  describe('getApiKeys', () => {
    test('fetches API keys from endpoint', async () => {
      const mockResponse = {
        keys: [
          { id: 'key-1', name: 'Test Key', key_prefix: 'abc', is_active: true, created_at: '2024-01-01' }
        ]
      };

      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(mockResponse)
      });

      const result = await getApiKeys();

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/api-keys',
        expect.objectContaining({
          headers: expect.objectContaining({
            'Content-Type': 'application/json',
            'X-Authorization': 'Bearer test-token'
          })
        })
      );
      expect(result).toEqual(mockResponse);
    });

    test('throws error on API failure', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 401,
        json: () => Promise.resolve({ error: 'Unauthorized' })
      });

      await expect(getApiKeys()).rejects.toThrow('Unauthorized');
    });
  });

  describe('getApiKeysUsageStats', () => {
    test('fetches section-level usage summary', async () => {
      const mockResponse = {
        total_active: 2,
        total_requests_24h: 42,
        total_requests_lifetime: 1234,
        top_keys: [
          { id: 'key-1', name: 'Busy', key_prefix: 'abc12345', request_count_24h: 30 },
        ],
      };

      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(mockResponse),
      });

      const result = await getApiKeysUsageStats();

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/api-keys/usage-stats',
        expect.objectContaining({
          headers: expect.objectContaining({
            'X-Authorization': 'Bearer test-token',
          }),
        })
      );
      expect(result).toEqual(mockResponse);
    });

    test('throws error on API failure', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 401,
        json: () => Promise.resolve({ error: 'Unauthorized' }),
      });

      await expect(getApiKeysUsageStats()).rejects.toThrow('Unauthorized');
    });
  });

  describe('createApiKey', () => {
    test('creates API key with name only', async () => {
      const mockResponse = {
        api_key: 'full-key-value',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'New Key', key_prefix: 'xyz', is_active: true, created_at: '2024-01-01' }
      };

      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(mockResponse)
      });

      const result = await createApiKey({ name: 'New Key' });

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/api-keys',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ name: 'New Key' }),
          headers: expect.objectContaining({
            'Content-Type': 'application/json',
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
      expect(result).toEqual(mockResponse);
    });

    test('creates API key with permissions and expiration', async () => {
      const mockResponse = {
        api_key: 'full-key-value',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'New Key', key_prefix: 'xyz', is_active: true, created_at: '2024-01-01' }
      };

      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(mockResponse)
      });

      const request = {
        name: 'Admin Key',
        permissions: [{ action: 'read', resource: '*' }],
        expires_at: '2025-12-31T00:00:00Z'
      };

      const result = await createApiKey(request);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/api-keys',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(request)
        })
      );
      expect(result).toEqual(mockResponse);
    });

    test('throws error on API failure', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 400,
        json: () => Promise.resolve({ error: 'Invalid request' })
      });

      await expect(createApiKey({ name: '' })).rejects.toThrow('Invalid request');
    });
  });

  describe('revokeApiKey', () => {
    test('revokes API key by ID', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await revokeApiKey('key-123');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/api-keys/key-123/revoke',
        expect.objectContaining({
          method: 'POST',
          headers: expect.objectContaining({
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
    });

    test('throws error on API failure', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 404,
        json: () => Promise.resolve({ error: 'Key not found' })
      });

      await expect(revokeApiKey('non-existent')).rejects.toThrow('Key not found');
    });
  });

  describe('deleteApiKey', () => {
    test('deletes API key by ID', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await deleteApiKey('key-456');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/api-keys/key-456',
        expect.objectContaining({
          method: 'DELETE',
          headers: expect.objectContaining({
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
    });

    test('throws error on API failure', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 403,
        json: () => Promise.resolve({ error: 'Permission denied' })
      });

      await expect(deleteApiKey('key-456')).rejects.toThrow('Permission denied');
    });
  });
});
