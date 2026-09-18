import { createFileRoute } from "@tanstack/react-router";
import { CommandSurface } from "#/features/command/command-surface";

export const Route = createFileRoute("/")({ component: CommandSurface });
