import { randomBytes, createHash, timingSafeEqual } from 'node:crypto'
import { createServer } from 'node:http'

const random = () => randomBytes(32).toString('base64url')
const digest = (value) => createHash('sha256').update(value).digest('base64url')
const equal = (left, right) => {
  if (typeof left !== 'string' || typeof right !== 'string') return false
  const a = Buffer.from(left)
  const b = Buffer.from(right)
  return a.length === b.length && timingSafeEqual(a, b)
}
const escapeHTML = (value) =>
  String(value).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;')

function subject() {
  return BigInt(`0x${randomBytes(12).toString('hex')}`).toString(10)
}

/** A real controlled OAuth 2.0 authorization-code provider with isolated subjects. */
export async function createB5OAuth({ callbackBases, secrets, identities }) {
  if (!Array.isArray(callbackBases) || !callbackBases.length)
    throw new Error('B5 OAuth provider requires registered callback bases')
  if (!Array.isArray(identities) || identities.length < 4)
    throw new Error('B5 OAuth provider requires multiple isolated test identities')

  const clientID = 'b5-controlled'
  const clientSecret = random()
  const password = random()
  secrets.push(clientSecret, password)
  const users = new Map(identities.map((username) => [username, { id: subject(), username }]))
  if (users.size !== identities.length) throw new Error('B5 OAuth identity names must be unique')

  const authorizations = new Map()
  const codes = new Map()
  const tokens = new Map()
  const events = []
  const errors = []
  let base

  function sendJSON(response, status, value) {
    response.writeHead(status, {
      'Cache-Control': 'no-store',
      'Content-Type': 'application/json; charset=utf-8',
      Pragma: 'no-cache',
    })
    response.end(JSON.stringify(value))
  }

  async function readForm(request) {
    if (!/^application\/x-www-form-urlencoded(?:;|$)/u.test(request.headers['content-type'] ?? ''))
      throw new Error('invalid form content type')
    let body = ''
    for await (const chunk of request) {
      body += chunk
      if (body.length > 16_384) throw new Error('form too large')
    }
    return new URLSearchParams(body)
  }

  const server = createServer(async (request, response) => {
    try {
      const url = new URL(request.url, base)
      if (url.pathname === '/authorize' && request.method === 'GET') {
        const query = url.searchParams
        const redirectURI = query.get('redirect_uri')
        if (
          query.get('client_id') !== clientID ||
          query.get('response_type') !== 'code' ||
          query.get('code_challenge_method') !== 'S256' ||
          !/^[A-Za-z0-9_-]{43}$/u.test(query.get('code_challenge') ?? '') ||
          !query.get('state') ||
          !callbackBases.some(
            (candidate) =>
              redirectURI?.startsWith(`${candidate}/api/v1/auth/external/`) &&
              redirectURI.endsWith('/callback'),
          )
        )
          throw new Error('invalid authorization request')

        const key = random()
        authorizations.set(key, {
          clientID,
          redirectURI,
          state: query.get('state'),
          challenge: query.get('code_challenge'),
        })
        response.writeHead(200, {
          'Cache-Control': 'no-store',
          'Content-Type': 'text/html; charset=utf-8',
          'Referrer-Policy': 'no-referrer',
        })
        response.end(
          `<!doctype html><html lang="en"><meta charset="utf-8"><title>Controlled OAuth</title><h1>Authorize test identity</h1><form action="/approve" method="post"><input type="hidden" name="authorization" value="${escapeHTML(key)}"><label>Username <input id="username" name="username" required autocomplete="username"></label><label>Password <input id="password" name="password" type="password" required autocomplete="current-password"></label><button id="oauth-approve" name="decision" value="allow">Authorize</button><button id="oauth-deny" name="decision" value="deny" formnovalidate>Deny</button></form></html>`,
        )
        return
      }

      if (url.pathname === '/approve' && request.method === 'POST') {
        const form = await readForm(request)
        const authorization = form.get('authorization')
        const transaction = authorizations.get(authorization)
        if (!transaction) throw new Error('authorization expired')
        authorizations.delete(authorization)
        const redirect = new URL(transaction.redirectURI)
        redirect.searchParams.set('state', transaction.state)
        if (form.get('decision') === 'deny') {
          redirect.searchParams.set('error', 'access_denied')
        } else {
          const user = users.get(form.get('username'))
          if (!user || !equal(form.get('password'), password))
            throw new Error('invalid controlled identity credentials')
          const code = random()
          codes.set(code, { ...transaction, username: user.username })
          secrets.push(code)
          redirect.searchParams.set('code', code)
        }
        response.writeHead(303, {
          'Cache-Control': 'no-store',
          Location: redirect.href,
          'Referrer-Policy': 'no-referrer',
        })
        response.end()
        return
      }

      if (url.pathname === '/token' && request.method === 'POST') {
        const form = await readForm(request)
        const code = form.get('code')
        const transaction = codes.get(code)
        if (!transaction) throw new Error('authorization code expired or consumed')
        codes.delete(code)
        if (
          form.get('grant_type') !== 'authorization_code' ||
          form.get('redirect_uri') !== transaction.redirectURI ||
          !/^[A-Za-z0-9._~-]{43,128}$/u.test(form.get('code_verifier') ?? '') ||
          digest(form.get('code_verifier')) !== transaction.challenge
        )
          throw new Error('invalid authorization code exchange')

        const basic = request.headers.authorization?.replace(/^Basic /u, '')
        const credentials = basic ? Buffer.from(basic, 'base64').toString('utf8') : ''
        if (
          !equal(
            credentials,
            `${encodeURIComponent(clientID)}:${encodeURIComponent(clientSecret)}`,
          ) ||
          form.has('client_id') ||
          form.has('client_secret')
        )
          throw new Error('invalid OAuth client authentication')

        const token = random()
        tokens.set(token, { username: transaction.username })
        secrets.push(token)
        events.push({ phase: 'token', username: transaction.username, pkce: 'S256' })
        sendJSON(response, 200, { access_token: token, token_type: 'Bearer', expires_in: 60 })
        return
      }

      if (url.pathname === '/user' && request.method === 'GET') {
        if (url.search) throw new Error('userinfo query parameters are not supported')
        const token = request.headers.authorization?.replace(/^Bearer /u, '')
        const authorization = tokens.get(token)
        if (!authorization) throw new Error('access token missing or consumed')
        tokens.delete(token)
        const user = users.get(authorization.username)
        const verifiedAt = Math.floor(Date.now() / 1000)
        const profile = {
          login: user.username,
          name: user.username,
          email: `${user.username}@example.invalid`,
          email_verified: true,
          auth_time: verifiedAt,
          created_at: '2010-01-02T03:04:05Z',
        }
        events.push({ phase: 'profile', username: user.username, subject: user.id })
        response.writeHead(200, {
          'Cache-Control': 'no-store',
          'Content-Type': 'application/json; charset=utf-8',
        })
        response.end(`{"id":${user.id},${JSON.stringify(profile).slice(1)}`)
        return
      }

      sendJSON(response, 404, { error: 'not_found' })
    } catch {
      errors.push({ method: request.method, path: new URL(request.url, base).pathname })
      sendJSON(response, 400, { error: 'invalid_request' })
    }
  })

  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  base = `http://127.0.0.1:${server.address().port}`
  return { base, clientID, clientSecret, password, server, events, errors, users }
}
