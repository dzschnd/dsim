type NodeTerminalProps = {
	terminalLines: string[];
	terminalInput: string;
	onInputChange: (value: string) => void;
	onSubmit: () => void;
};

export function NodeTerminal({
	terminalLines,
	terminalInput,
	onInputChange,
	onSubmit,
}: NodeTerminalProps) {
	return (
		<div className="nodrag nopan nowheel absolute bottom-full left-1/2 z-[1000] mb-2 flex h-44 w-64 -translate-x-1/2 flex-col overflow-hidden rounded border border-slate-800 bg-zinc-950 text-left font-mono text-[8px] text-zinc-100 shadow-lg">
			<div
				className="node-terminal-scroll nowheel flex-1 overflow-y-auto px-3 py-2"
				onWheel={(event) => {
					event.stopPropagation();
				}}
			>
				{terminalLines.length > 0
					? terminalLines.map((line, index) => (
						<div
							key={`${line}-${index}`}
							className="break-words whitespace-pre-wrap leading-5 text-zinc-300"
						>
							{line}
						</div>
					))
					: null}
			</div>
			<div className="flex items-center gap-2 border-t border-zinc-800 px-3 py-2">
				<span className="text-emerald-400">$</span>
				<input
					type="text"
					value={terminalInput}
					onChange={(event) => {
						event.stopPropagation();
						onInputChange(event.target.value);
					}}
					onClick={(event) => {
						event.stopPropagation();
					}}
					onPointerDown={(event) => {
						event.stopPropagation();
					}}
					onKeyDown={(event) => {
						event.stopPropagation();
						if (event.key === "Enter") {
							event.preventDefault();
							onSubmit();
						}
					}}
					className="nodrag nopan w-full border-none bg-transparent p-0 text-zinc-100 outline-none placeholder:text-zinc-600"
					placeholder="enter command"
				/>
			</div>
		</div>
	);
}
