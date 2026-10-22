import type { ApiSchemas } from './client-core'

const object = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === 'object' && !Array.isArray(value)

export const isSignupRequirements = (
  value: unknown,
): value is ApiSchemas['SignupPolicyRequirements'] =>
  object(value) &&
  (value.email_requirement === 'optional' || value.email_requirement === 'required') &&
  ['none', 'optional', 'required'].includes(value.password_requirement as string)

const hasSignupSteps = (value: Record<string, unknown>): boolean =>
  Array.isArray(value.signup_requirements) &&
  value.signup_requirements.length <= 2 &&
  value.signup_requirements.every((step) => step === 'email' || step === 'local_password') &&
  new Set(value.signup_requirements).size === value.signup_requirements.length

const signupStepsMatch = (value: Record<string, unknown>): boolean =>
  isSignupRequirements(value.signup) &&
  hasSignupSteps(value) &&
  (value.signup_requirements as string[]).includes('email') ===
    (value.signup.email_requirement === 'required') &&
  (value.signup_requirements as string[]).includes('local_password') ===
    (value.signup.password_requirement === 'required')

export const isExternalConfirmation = (
  value: unknown,
): value is ApiSchemas['ExternalConfirmationResponse'] =>
  object(value) &&
  (value.kind === 'signup_confirmation' || value.kind === 'link_confirmation') &&
  typeof value.confirmation_token === 'string' &&
  value.confirmation_token.length > 0 &&
  typeof value.transaction_id === 'string' &&
  value.transaction_id.length > 0 &&
  typeof value.expires_at === 'string' &&
  Number.isFinite(Date.parse(value.expires_at)) &&
  hasSignupSteps(value) &&
  (value.kind === 'link_confirmation'
    ? (value.signup_requirements as string[]).length === 0
    : signupStepsMatch(value)) &&
  object(value.profile) &&
  ['connection_id', 'connection_name', 'external_display'].every(
    (key) =>
      typeof value.profile === 'object' &&
      typeof (value.profile as Record<string, unknown>)[key] === 'string',
  )

export const isLocalAccountCapabilities = (
  value: unknown,
): value is ApiSchemas['LocalAccountCapabilities'] =>
  object(value) &&
  [
    'can_login',
    'can_signup',
    'can_enroll_password',
    'password_login_enabled',
    'username_login_enabled',
    'email_login_enabled',
    'password_enrollment_enabled',
  ].every((key) => typeof value[key] === 'boolean') &&
  isSignupRequirements(value.signup) &&
  value.signup.password_requirement === 'required' &&
  signupStepsMatch(value)

export const isAuthCapabilities = (value: unknown): value is ApiSchemas['AuthCapabilities'] =>
  object(value) &&
  (value.mode === 'local' || value.mode === 'server') &&
  isLocalAccountCapabilities(value.local_account) &&
  value.email_verification_enabled === false &&
  value.password_reset_enabled === false &&
  Array.isArray(value.connections) &&
  value.connections.every(
    (connection: unknown) =>
      object(connection) &&
      typeof connection.connection_id === 'string' &&
      !!connection.connection_id &&
      typeof connection.display_name === 'string' &&
      typeof connection.description === 'string' &&
      Number.isSafeInteger(connection.display_order) &&
      typeof connection.can_login === 'boolean' &&
      typeof connection.can_signup === 'boolean' &&
      isSignupRequirements(connection.signup) &&
      signupStepsMatch(connection),
  )

export const isAuthAttributeCatalog = (
  value: unknown,
  purpose: ApiSchemas['AuthAttributeCatalog']['purpose'],
  revision: number,
): value is ApiSchemas['AuthAttributeCatalog'] =>
  object(value) &&
  value.purpose === purpose &&
  value.revision === revision &&
  object(value.operators) &&
  Array.isArray(value.fields) &&
  value.fields.every(
    (field: unknown) =>
      object(field) &&
      typeof field.field_id === 'string' &&
      typeof field.label === 'string' &&
      typeof field.type === 'string' &&
      ['string', 'boolean', 'integer', 'number', 'datetime', 'string_list'].includes(field.type) &&
      typeof field.source === 'string' &&
      ['oidc_id_token', 'oidc_userinfo', 'oauth2_profile', 'registration', 'derived'].includes(
        field.source,
      ) &&
      typeof field.pointer === 'string' &&
      typeof field.user_editable === 'boolean' &&
      Array.isArray(field.allowed_operators) &&
      field.allowed_operators.every((operator: unknown) => typeof operator === 'string'),
  )

export const isLoginMethods = (value: unknown): value is ApiSchemas['LoginMethodsResponse'] =>
  object(value) &&
  object(value.password) &&
  ['is_set', 'can_login', 'can_set', 'can_delete', 'verified'].every(
    (key) => typeof (value.password as Record<string, unknown>)[key] === 'boolean',
  ) &&
  object(value.email) &&
  (value.email.address === null || typeof value.email.address === 'string') &&
  typeof value.email.verified === 'boolean' &&
  typeof value.email.can_recover === 'boolean' &&
  Array.isArray(value.bindings) &&
  value.bindings.every(
    (binding: unknown) =>
      object(binding) &&
      Number.isSafeInteger(binding.binding_id) &&
      Number(binding.binding_id) > 0 &&
      typeof binding.connection_id === 'string' &&
      !!binding.connection_id &&
      typeof binding.display_name === 'string' &&
      ['can_login', 'can_reauthenticate', 'can_unlink', 'verified', 'archived'].every(
        (key) => typeof binding[key] === 'boolean',
      ),
  ) &&
  Array.isArray(value.available_connections) &&
  value.available_connections.every(
    (connection: unknown) =>
      object(connection) &&
      typeof connection.connection_id === 'string' &&
      !!connection.connection_id &&
      typeof connection.display_name === 'string' &&
      typeof connection.can_link === 'boolean',
  )

export const isReauthenticationProof = (
  value: unknown,
): value is ApiSchemas['ReauthenticationProofResponse'] =>
  object(value) &&
  typeof value.reauthentication_proof === 'string' &&
  value.reauthentication_proof.length > 0 &&
  typeof value.expires_at === 'string' &&
  Number.isFinite(Date.parse(value.expires_at))
