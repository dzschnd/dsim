import type { SVGProps } from "react";

export default function HostIcon(props: SVGProps<SVGSVGElement>) {
	return (
		<svg
			version="1.1"
			xmlns="http://www.w3.org/2000/svg"
			viewBox="36 56 440 388"
			preserveAspectRatio="xMidYMid meet"
			aria-hidden="true"
			{...props}
		>
			<path
				fill="#000000"
				fillRule="evenodd"
				d="M56 56
					H456
					C467.045695 56 476 64.954305 476 76
					V316
					C476 327.045695 467.045695 336 456 336
					H56
					C44.954305 336 36 327.045695 36 316
					V76
					C36 64.954305 44.954305 56 56 56
					Z
					M56 76
					V316
					H456
					V76
					Z"
				clipRule="evenodd"
			/>
			<path
				fill="#000000"
				d="M226 336 H286 V404 H226 Z
					M156 404 H356
					C367.045695 404 376 412.954305 376 424
					C376 435.045695 367.045695 444 356 444
					H156
					C144.954305 444 136 435.045695 136 424
					C136 412.954305 144.954305 404 156 404
					Z"
			/>
		</svg>
	);
}
