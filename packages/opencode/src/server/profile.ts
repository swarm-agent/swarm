import { Hono } from "hono"
import { describeRoute, resolver, validator } from "hono-openapi"
import z from "zod"
import { Profile } from "../profile"
import { Config } from "../config/config"
import { streamSSE } from "hono/streaming"
import { Bus } from "../bus"
import { ContainerRuntime } from "../container/runtime"

const ERRORS = {
  400: {
    description: "Bad request",
    content: {
      "application/json": {
        schema: resolver(
          z.object({
            data: z.any().nullable(),
            errors: z.array(z.record(z.string(), z.any())),
            success: z.literal(false),
          }),
        ),
      },
    },
  },
  404: {
    description: "Not found",
    content: {
      "application/json": {
        schema: resolver(Profile.NotFoundError.Schema),
      },
    },
  },
}

function errors(...codes: number[]) {
  return Object.fromEntries(codes.map((code) => [code, ERRORS[code as keyof typeof ERRORS]]))
}

export const ProfileRoute = new Hono()
  .get(
    "/",
    describeRoute({
      description: "List all profiles",
      operationId: "profile.list",
      responses: {
        200: {
          description: "List of profiles",
          content: {
            "application/json": {
              schema: resolver(z.array(Profile.Info)),
            },
          },
        },
      },
    }),
    async (c) => {
      return c.json(await Profile.list())
    },
  )
  .post(
    "/",
    describeRoute({
      description: "Create a new profile",
      operationId: "profile.create",
      responses: {
        200: {
          description: "Created profile",
          content: {
            "application/json": {
              schema: resolver(Profile.Info),
            },
          },
        },
        ...errors(400),
      },
    }),
    validator("json", Config.ContainerProfileConfig),
    async (c) => {
      const config = c.req.valid("json")
      const profile = await Profile.create(config)
      return c.json(profile)
    },
  )
  .get(
    "/:name",
    describeRoute({
      description: "Get a profile by name",
      operationId: "profile.get",
      responses: {
        200: {
          description: "Profile details",
          content: {
            "application/json": {
              schema: resolver(Profile.Info),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    async (c) => {
      const { name } = c.req.valid("param")
      const profile = await Profile.get(name)
      if (!profile) {
        throw new Profile.NotFoundError({ name })
      }
      return c.json(profile)
    },
  )
  .patch(
    "/:name",
    describeRoute({
      description: "Update a profile",
      operationId: "profile.update",
      responses: {
        200: {
          description: "Updated profile",
          content: {
            "application/json": {
              schema: resolver(Profile.Info),
            },
          },
        },
        ...errors(400, 404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    validator("json", Config.ContainerProfileConfig.partial()),
    async (c) => {
      const { name } = c.req.valid("param")
      const config = c.req.valid("json")
      const profile = await Profile.update(name, config)
      return c.json(profile)
    },
  )
  .delete(
    "/:name",
    describeRoute({
      description: "Delete a profile",
      operationId: "profile.delete",
      responses: {
        200: {
          description: "Profile deleted",
          content: {
            "application/json": {
              schema: resolver(z.boolean()),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    async (c) => {
      const { name } = c.req.valid("param")
      await Profile.remove(name)
      return c.json(true)
    },
  )
  .post(
    "/:name/start",
    describeRoute({
      description: "Start a profile's container",
      operationId: "profile.start",
      responses: {
        200: {
          description: "Container started",
          content: {
            "application/json": {
              schema: resolver(z.object({ containerID: z.string() })),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    validator("json", z.object({ pull: z.boolean().optional() }).optional()),
    async (c) => {
      const { name } = c.req.valid("param")
      const body = c.req.valid("json")

      if (body?.pull) {
        const profile = await Profile.get(name)
        if (profile) {
          const config = await Config.get()
          const runtime = config.container?.runtime ?? "podman"
          await ContainerRuntime.pull(runtime, profile.config.image)
        }
      }

      const containerID = await Profile.start(name)
      return c.json({ containerID })
    },
  )
  .post(
    "/:name/stop",
    describeRoute({
      description: "Stop a profile's container",
      operationId: "profile.stop",
      responses: {
        200: {
          description: "Container stopped",
          content: {
            "application/json": {
              schema: resolver(z.boolean()),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    async (c) => {
      const { name } = c.req.valid("param")
      await Profile.stop(name)
      return c.json(true)
    },
  )
  .post(
    "/:name/restart",
    describeRoute({
      description: "Restart a profile's container",
      operationId: "profile.restart",
      responses: {
        200: {
          description: "Container restarted",
          content: {
            "application/json": {
              schema: resolver(z.object({ containerID: z.string() })),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    async (c) => {
      const { name } = c.req.valid("param")
      const containerID = await Profile.restart(name)
      return c.json({ containerID })
    },
  )
  .get(
    "/:name/status",
    describeRoute({
      description: "Get container status for a profile",
      operationId: "profile.status",
      responses: {
        200: {
          description: "Container status",
          content: {
            "application/json": {
              schema: resolver(
                z.object({
                  status: z.enum(["stopped", "starting", "running", "error"]),
                  containerID: z.string().optional(),
                  running: z.boolean(),
                }),
              ),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    async (c) => {
      const { name } = c.req.valid("param")
      const status = await Profile.status(name)
      return c.json(status)
    },
  )
  .get(
    "/:name/logs",
    describeRoute({
      description: "Stream container logs",
      operationId: "profile.logs",
      responses: {
        200: {
          description: "Log stream",
          content: {
            "text/event-stream": {
              schema: resolver(z.string()),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    validator(
      "query",
      z.object({
        follow: z.coerce.boolean().optional(),
        tail: z.coerce.number().optional(),
      }),
    ),
    async (c) => {
      const { name } = c.req.valid("param")
      const { follow, tail } = c.req.valid("query")

      const profile = await Profile.get(name)
      if (!profile || !profile.containerID) {
        throw new Profile.NotFoundError({ name })
      }

      return streamSSE(c, async (stream) => {
        for await (const line of ContainerRuntime.logs(profile.containerID!, { follow, tail })) {
          await stream.writeSSE({ data: line })
        }
      })
    },
  )
  .post(
    "/:name/exec",
    describeRoute({
      description: "Execute a command in a profile's container",
      operationId: "profile.exec",
      responses: {
        200: {
          description: "Execution result",
          content: {
            "application/json": {
              schema: resolver(
                z.object({
                  exitCode: z.number(),
                  stdout: z.string(),
                  stderr: z.string(),
                }),
              ),
            },
          },
        },
        ...errors(404),
      },
    }),
    validator("param", z.object({ name: z.string() })),
    validator("json", z.object({ command: z.array(z.string()) })),
    async (c) => {
      const { name } = c.req.valid("param")
      const { command } = c.req.valid("json")
      const result = await Profile.exec(name, command)
      return c.json(result)
    },
  )
  .get(
    "/:name/event",
    describeRoute({
      description: "Subscribe to profile events",
      operationId: "profile.events",
      responses: {
        200: {
          description: "Event stream",
          content: {
            "text/event-stream": {
              schema: resolver(z.any()),
            },
          },
        },
      },
    }),
    validator("param", z.object({ name: z.string() })),
    async (c) => {
      const { name } = c.req.valid("param")

      return streamSSE(c, async (stream) => {
        const unsub = Bus.subscribeAll(async (event) => {
          // Filter to only events for this profile
          if (
            event.type.startsWith("profile.") &&
            (event.properties as any)?.name === name
          ) {
            await stream.writeSSE({ data: JSON.stringify(event) })
          }
        })

        await new Promise<void>((resolve) => {
          stream.onAbort(() => {
            unsub()
            resolve()
          })
        })
      })
    },
  )
