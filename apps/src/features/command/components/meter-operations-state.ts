import type {
	CommandDetail,
	CommandTemplate,
	MeterOperations,
	ReconciliationCase,
	ReconciliationItem,
} from "#/api/meter-operations";

export type ListMode = "open" | "resolved";
export interface PanelState {
	credential: string;
	session: { client: MeterOperations; templates: CommandTemplate[] } | null;
	list: {
		mode: ListMode;
		queue: ReconciliationItem[];
		archive: ReconciliationCase[];
		nextQueue: string;
		nextArchive: string;
	};
	detail: CommandDetail | null;
	search: string;
	command: {
		type: string;
		meter: string;
		reason: string;
		confirmation: string;
	};
	busy: boolean;
	error: string;
}
export function initialPanelState(): PanelState {
	return {
		credential: "",
		session: null,
		list: {
			mode: "open",
			queue: [],
			archive: [],
			nextQueue: "",
			nextArchive: "",
		},
		detail: null,
		search: "",
		command: {
			type: "request_meter_status",
			meter: "",
			reason: "Investigate meter state",
			confirmation: "",
		},
		busy: false,
		error: "",
	};
}
export type PanelAction =
	| { type: "credential" | "search"; value: string }
	| { type: "started" | "finished" | "signedOut" }
	| { type: "failed"; message: string }
	| { type: "connected"; client: MeterOperations; templates: CommandTemplate[] }
	| { type: "mode"; mode: ListMode }
	| {
			type: "queueLoaded";
			items: ReconciliationItem[];
			next: string;
			append: boolean;
	  }
	| {
			type: "archiveLoaded";
			items: ReconciliationCase[];
			next: string;
			append: boolean;
	  }
	| { type: "detailLoaded"; detail: CommandDetail }
	| { type: "template"; value: string }
	| {
			type: "commandField";
			field: "meter" | "reason" | "confirmation";
			value: string;
	  };

// Coupled transitions live here: session replacement clears prior operator data;
// selecting a target clears relay confirmation; templates supply their reason.
export function panelReducer(
	state: PanelState,
	action: PanelAction,
): PanelState {
	switch (action.type) {
		case "credential":
			return { ...state, credential: action.value };
		case "search":
			return { ...state, search: action.value };
		case "started":
			return { ...state, busy: true, error: "" };
		case "finished":
			return { ...state, busy: false };
		case "failed":
			return { ...state, error: action.message };
		case "signedOut":
			return initialPanelState();
		case "connected":
			return {
				...initialPanelState(),
				busy: true,
				session: { client: action.client, templates: action.templates },
			};
		case "mode":
			return { ...state, list: { ...state.list, mode: action.mode } };
		case "queueLoaded":
			return {
				...state,
				list: {
					...state.list,
					queue: action.append
						? [...state.list.queue, ...action.items]
						: action.items,
					nextQueue: action.next,
				},
			};
		case "archiveLoaded":
			return {
				...state,
				list: {
					...state.list,
					archive: action.append
						? [...state.list.archive, ...action.items]
						: action.items,
					nextArchive: action.next,
				},
			};
		case "detailLoaded":
			return {
				...state,
				detail: action.detail,
				command: {
					...state.command,
					meter: action.detail.command.meter_id,
					confirmation: "",
				},
			};
		case "template":
			return {
				...state,
				command: {
					...state.command,
					type: action.value,
					reason:
						state.session?.templates.find(
							(t) => t.command_type === action.value,
						)?.default_reason ?? "",
					confirmation: "",
				},
			};
		case "commandField":
			return {
				...state,
				command: {
					...state.command,
					[action.field]: action.value,
					...(action.field === "meter" ? { confirmation: "" } : {}),
				},
			};
	}
}

export function formatCaseAge(seconds: number): string {
	const hours = Math.floor(seconds / 3600);
	return hours < 24 ? `${hours}h` : `${Math.floor(hours / 24)}d`;
}
