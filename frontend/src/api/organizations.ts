import { apiClient, type ApiClient, type ApiSchemas } from './client-core'
import { buildRequestFailureError } from './utils'
import { t } from '@/i18n'

export async function fetchOrganizations(client: ApiClient = apiClient) {
  const { data, error, response } = await client.GET('/orgs')
  if (!data) throw buildRequestFailureError(t('team.errors.load'), error, response)
  return data
}
export async function fetchOrganization(orgId: number, client: ApiClient = apiClient) {
  const { data, error, response } = await client.GET('/orgs/{orgId}', {
    params: { path: { orgId } },
  })
  if (!data) throw buildRequestFailureError(t('team.errors.load'), error, response)
  return data
}
export async function createOrganization(
  body: ApiSchemas['OrganizationRequest'],
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST('/orgs', { body })
  if (!data) throw buildRequestFailureError(t('team.errors.save'), error, response)
  return data
}
export async function updateOrganization(
  orgId: number,
  body: ApiSchemas['OrganizationRequest'],
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.PUT('/orgs/{orgId}', {
    params: { path: { orgId } },
    body,
  })
  if (!data) throw buildRequestFailureError(t('team.errors.save'), error, response)
  return data
}
export async function fetchOrganizationMembers(orgId: number, client: ApiClient = apiClient) {
  const { data, error, response } = await client.GET('/orgs/{orgId}/members', {
    params: { path: { orgId } },
  })
  if (!data) throw buildRequestFailureError(t('team.errors.members'), error, response)
  return data
}
export async function addOrganizationMember(
  orgId: number,
  body: ApiSchemas['AddOrganizationMemberRequest'],
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST('/orgs/{orgId}/members', {
    params: { path: { orgId } },
    body,
  })
  if (!data) throw buildRequestFailureError(t('team.errors.memberSave'), error, response)
  return data
}
export async function updateOrganizationMember(
  orgId: number,
  userId: number,
  role: ApiSchemas['OrganizationMember']['role'],
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.PUT('/orgs/{orgId}/members/{userId}', {
    params: { path: { orgId, userId } },
    body: { role },
  })
  if (!data) throw buildRequestFailureError(t('team.errors.memberSave'), error, response)
  return data
}
export async function removeOrganizationMember(
  orgId: number,
  userId: number,
  client: ApiClient = apiClient,
) {
  const { error, response } = await client.DELETE('/orgs/{orgId}/members/{userId}', {
    params: { path: { orgId, userId } },
  })
  if (!response.ok) throw buildRequestFailureError(t('team.errors.memberSave'), error, response)
}
export async function fetchOrganizationActivity(orgId: number, client: ApiClient = apiClient) {
  const { data, error, response } = await client.GET('/activity', {
    params: { query: { org_id: orgId, limit: 10 } },
  })
  if (!data) throw buildRequestFailureError(t('team.errors.activity'), error, response)
  return data
}
