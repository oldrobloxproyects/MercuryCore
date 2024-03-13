import { authorise } from "$lib/server/lucia"
import { auditLog, stuff } from "$lib/server/orm"
import ratelimit from "$lib/server/ratelimit"
import formError from "$lib/server/formError"
import { superValidate, message } from "sveltekit-superforms/server"
import { zod } from "sveltekit-superforms/adapters"
import { z } from "zod"

const schema = z.object({
	dailyStipend: z.number().int().positive().max(100),
	stipendTime: z.number().min(1),
})

export async function load({ locals }) {
	await authorise(locals, 5)

	const economy = await stuff.select1(
		"economy",
		"dailyStipend",
		"stipendTime"
	)

	return {
		form: await superValidate(zod(schema)),
		dailyStipend: economy?.dailyStipend || 10,
		stipendTime: economy?.stipendTime || 12,
	}
}

export const actions = {
	updateStipend: async ({ request, locals, getClientAddress }) => {
		const { user } = await authorise(locals, 5)
		const form = await superValidate(request, zod(schema))
		if (!form.valid) return formError(form)

		const limit = ratelimit(form, "economy", getClientAddress, 30)
		if (limit) return limit

		const economy = await stuff.select1(
			"economy",
			"dailyStipend",
			"stipendTime"
		)
		const currentStipend = economy?.dailyStipend || 10
		const currentStipendTime = economy?.stipendTime || 12
		const { dailyStipend, stipendTime } = form.data

		if (
			currentStipend === dailyStipend &&
			currentStipendTime === stipendTime
		)
			return message(form, "No changes were made")

		await stuff.merge("economy", { dailyStipend, stipendTime })

		let auditText = ""

		if (currentStipend !== dailyStipend)
			auditText += `Change daily stipend from ${currentStipend} to ${dailyStipend}`

		if (currentStipendTime !== stipendTime) {
			if (auditText) auditText += ", "
			auditText += `Change stipend time from ${currentStipendTime} to ${stipendTime}`
		}

		await auditLog.create({
			action: "Economy",
			note: auditText,
			user: `user:${user.id}`,
		})

		return message(form, "Economy updated successfully!")
	},
}
