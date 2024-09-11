// "'Hooks' are app-wide functions you declare that SvelteKit will call in response to specific events, giving you fine-grained control over the framework's behaviour."
// See https://kit.svelte.dev/docs/hooks/ for more info.

import { i18n } from "$lib/i18n"
import config from "$lib/server/config"
import { stipend } from "$lib/server/economy"
import { auth } from "$lib/server/lucia"
import { Record, equery, surql } from "$lib/server/surreal"
import { type Handle, redirect } from "@sveltejs/kit"
import { sequence } from "@sveltejs/kit/hooks"
import type { Cookie, User } from "lucia"
import pc from "picocolors"

const { magenta, red, yellow, green, blue, gray } = pc
const methodColours = Object.freeze({
	GET: green("GET"),
	POST: yellow("POST"),
})
const pathnameColours = Object.freeze({
	api: green,
	download: yellow,
	moderation: yellow,
	report: yellow,
	statistics: yellow,
	register: blue,
	login: blue,
	place: magenta,
	admin: red,
})

function pathnameColour(pathname: string) {
	for (const [prefix, colour] of Object.entries(pathnameColours))
		if (pathname.startsWith(`/${prefix}`)) return colour(pathname)
	return pathname
}

const time = () =>
	config.Logging.Time ? gray(new Date().toLocaleString()) : ""

const userLog = (user: User | null) =>
	user
		? blue(user.username) + " ".repeat(21 - user.username.length)
		: yellow("Logged-out user      ")

async function finish({ event, resolve }: Parameters<Handle>[0]) {
	const { pathname, search } = event.url
	const { user } = event.locals

	if (config.Logging.Requests) {
		// Fancy logging: time(?), user, method, and path
		const method = event.request.method as keyof typeof methodColours
		console.log(
			time(),
			userLog(user),
			methodColours[method] || method,
			" ".repeat(7 - method.length),
			pathnameColour(decodeURI(pathname) + search)
		)
	}

	const res = await resolve(event)
	if (!res.headers.get("content-type")?.includes("text/html")) return res

	// if it's html, add the user's theme before the </body> tag
	const file = Bun.file(`../Assets/${config.Themes[user?.theme || 0].Path}`)

	// ...and the custom CSS
	const css = user?.css ? `<style id="custom-css">${user.css}</style>` : ""

	// duplicate the response to avoid modifying the original
	const text = (await res.clone().text()).replace(
		"</body>",
		`<style>${await file.text()}</style>${css}</body>`
	)

	return new Response(text, {
		status: res.status,
		statusText: res.statusText,
		headers: res.headers,
	})
}

// Ran every time a dynamic request is made.
// Requests for prerendered pages do not trigger this hook.
async function handler(e) {
	const { event } = e

	const sessionId = event.cookies.get(auth.sessionCookieName)
	if (!sessionId) {
		event.locals.session = null
		event.locals.user = null
		return await finish(e)
	}

	const { session, user } = await auth.validateSession(sessionId)
	if (!session || !user) return await finish(e)

	event.locals.session = session
	event.locals.user = user

	const setSession = (sessionCookie: Cookie) =>
		// sveltekit types deviate from the de facto standard here
		event.cookies.set(sessionCookie.name, sessionCookie.value, {
			path: ".",
			...sessionCookie.attributes,
		})
	if (!session) setSession(auth.createBlankSessionCookie())
	else if (session.fresh) setSession(auth.createSessionCookie(session.id))

	const [, [moderated]] = await equery<1[][]>(
		surql`
			UPDATE $user SET lastOnline = time::now();
			SELECT 1 FROM moderation WHERE out = $user AND active`,
		{ user: Record("user", user.id) }
	)

	const { pathname } = event.url
	if (
		moderated &&
		!["/moderation", "/api"].includes(pathname) &&
		!pathname.startsWith("/api/avatar")
	)
		redirect(302, "/moderation")

	await stipend(user.id)
	return await finish(e)
}

export async function handleError({ event, error }) {
	const { user } = event.locals

	// Fancy error logging: time(?), user, and error
	if (!config.Logging.FormattedErrors) console.error(error)
	else console.error(time(), userLog(user), red(error as string))
}

export const handle = sequence(i18n.handle(), handler)
