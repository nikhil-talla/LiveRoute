export default function Logo({ size = "md" }: { size?: "sm" | "md" | "lg" }) {
  const sizes = { sm: "text-base", md: "text-xl", lg: "text-3xl" }
  const dotSizes = { sm: "w-1.5 h-1.5", md: "w-2 h-2", lg: "w-3 h-3" }
  return (
    <span className={`inline-flex items-center gap-1.5 font-extrabold tracking-tight ${sizes[size]} text-[#0C1A3A]`}>
      <span className="relative flex items-center justify-center">
        <svg
          width={size === "lg" ? 32 : size === "md" ? 24 : 18}
          height={size === "lg" ? 32 : size === "md" ? 24 : 18}
          viewBox="0 0 24 24"
          fill="none"
        >
          <circle cx="12" cy="12" r="11" fill="#1D4ED8" />
          <path
            d="M7 12 Q7 7 12 7 Q17 7 17 12"
            stroke="white"
            strokeWidth="1.8"
            strokeLinecap="round"
            fill="none"
          />
          <circle cx="12" cy="16" r="2.5" fill="white" />
          <circle cx="7" cy="12" r="1.5" fill="white" opacity="0.7" />
          <circle cx="17" cy="12" r="1.5" fill="white" opacity="0.7" />
        </svg>
      </span>
      LiveRoute
    </span>
  )
}
