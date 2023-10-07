import surql from "$lib/surrealtag"
import { squery } from "$lib/server/surreal"

type Places = {
	id: string
	name: string
	playerCount: number
	serverPing: number
}[]

export const load = () => ({
	places: squery(surql`
		SELECT
			string::split(type::string(id), ":")[1] AS id,
			name,
			serverPing,
			count(
				SELECT * FROM <-playing
				WHERE valid
					AND ping > time::now() - 35s
			) AS playerCount,
			count(<-likes) AS likeCount,
			count(<-dislikes) AS dislikeCount
		FROM place
		WHERE !privateServer AND !deleted`) as Promise<Places>,
})

export const actions = {
	default: async ({ request }) => ({
		places: (await squery(
			surql`
				SELECT
					string::split(type::string(id), ":")[1] AS id,
					name,
					serverPing,
					count(
						SELECT * FROM <-playing
						WHERE valid
							AND ping > time::now() - 35s
					) AS playerCount,
					count(<-likes) AS likeCount,
					count(<-dislikes) AS dislikeCount
				FROM place
				WHERE !privateServer
					AND !deleted
					AND string::lowercase($query) ∈ string::lowercase(name)`,
			{
				query: (await request.formData()).get("query") as string,
			},
		)) as Places,
	}),
}
