type Stop = { id: number; x: number; y: number; label: string }

const defaultStops: Stop[] = [
  { id: 1, x: 22, y: 65, label: "City Museum" },
  { id: 2, x: 38, y: 42, label: "Garden Café" },
  { id: 3, x: 58, y: 55, label: "Riverside Park" },
  { id: 4, x: 74, y: 30, label: "Old Town Hall" },
]

export default function MapPlaceholder({
  stops = defaultStops,
  showCurrentLocation = false,
  proposed = false,
  className = "",
}: {
  stops?: Stop[]
  showCurrentLocation?: boolean
  proposed?: boolean
  className?: string
}) {
  const path = stops
    .map((s, i) => `${i === 0 ? "M" : "L"} ${s.x} ${s.y}`)
    .join(" ")

  return (
    <div
      className={`relative overflow-hidden rounded-[12px] bg-[#E8F0FE] border border-[rgba(30,58,138,0.12)] ${className}`}
    >
      {/* Grid lines */}
      <svg
        className="absolute inset-0 w-full h-full opacity-30"
        preserveAspectRatio="none"
        viewBox="0 0 100 100"
      >
        {[10, 20, 30, 40, 50, 60, 70, 80, 90].map((v) => (
          <g key={v}>
            <line x1={v} y1="0" x2={v} y2="100" stroke="#93C5FD" strokeWidth="0.3" />
            <line x1="0" y1={v} x2="100" y2={v} stroke="#93C5FD" strokeWidth="0.3" />
          </g>
        ))}
      </svg>

      {/* Road-like lines */}
      <svg className="absolute inset-0 w-full h-full opacity-40" viewBox="0 0 100 100" preserveAspectRatio="none">
        <line x1="0" y1="50" x2="100" y2="50" stroke="#BFDBFE" strokeWidth="1.5" />
        <line x1="50" y1="0" x2="50" y2="100" stroke="#BFDBFE" strokeWidth="1.5" />
        <line x1="0" y1="25" x2="100" y2="75" stroke="#BFDBFE" strokeWidth="0.8" />
        <line x1="0" y1="75" x2="100" y2="35" stroke="#BFDBFE" strokeWidth="0.8" />
      </svg>

      <svg
        className="absolute inset-0 w-full h-full"
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
      >
        {/* Route line */}
        <path
          d={path}
          stroke={proposed ? "#94A3B8" : "#1D4ED8"}
          strokeWidth={proposed ? "0.8" : "1.2"}
          strokeDasharray={proposed ? "3 2" : "none"}
          fill="none"
          strokeLinecap="round"
          strokeLinejoin="round"
          opacity={proposed ? 0.7 : 1}
        />

        {/* Stop markers */}
        {stops.map((s) => (
          <g key={s.id}>
            <circle cx={s.x} cy={s.y} r="3" fill="white" stroke="#1D4ED8" strokeWidth="0.8" />
            <text
              x={s.x}
              y={s.y - 0.5}
              textAnchor="middle"
              dominantBaseline="middle"
              fontSize="1.8"
              fontWeight="700"
              fill="#1D4ED8"
              fontFamily="Plus Jakarta Sans, sans-serif"
            >
              {s.id}
            </text>
          </g>
        ))}

        {/* Current location */}
        {showCurrentLocation && (
          <g>
            <circle cx="20" cy="75" r="3.5" fill="#1D4ED8" opacity="0.15" />
            <circle cx="20" cy="75" r="2" fill="#1D4ED8" />
            <circle cx="20" cy="75" r="1" fill="white" />
          </g>
        )}
      </svg>

      {/* Map label */}
      <div className="absolute bottom-3 right-3 text-[10px] text-[#64748B] font-500 bg-white/80 px-2 py-1 rounded-md border border-[rgba(30,58,138,0.08)]">
        Route preview
      </div>

      {/* Zoom controls */}
      <div className="absolute top-3 right-3 flex flex-col rounded-lg border border-[rgba(30,58,138,0.12)] overflow-hidden bg-white shadow-sm">
        <button className="w-7 h-7 flex items-center justify-center text-[#64748B] hover:bg-[#F8FAFC] text-sm font-600 border-b border-[rgba(30,58,138,0.08)]">
          +
        </button>
        <button className="w-7 h-7 flex items-center justify-center text-[#64748B] hover:bg-[#F8FAFC] text-sm font-600">
          −
        </button>
      </div>
    </div>
  )
}
