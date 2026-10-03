import { ArrowLeft, ChevronDown, ChevronRight, FileKey, SlidersHorizontal, Timer } from 'lucide-react'
import { Link, NavLink } from 'react-router'
import { useDisclosureState } from '../lib/disclosures'
import { SHORT_VIEWPORT, useMediaQuery } from '../lib/media'
import { ACCESS_HOME, ADMIN_HOME, CREDENTIALS_HOME, PROFILE_HOME } from '../lib/navigation'

/* A row in the card: the navigation pill, a size down. */
const ROW =
  'nav-pill flex h-9 items-center gap-2.5 rounded-control px-2.5 text-left text-[13px] font-medium text-rail-muted'

/**
 * The template's plan card, about the person instead: who is signed in, and
 * the doors that are theirs. The name is the door to the account itself —
 * where everyone looks for it.
 *
 * On a short screen the two personal doors (My access, My credentials) fold
 * behind a chevron on the name row, closed until the person opens them and
 * remembered per person from then on: the card sits under the cluster tree and
 * every row it holds is a row the tree loses. Administration — and the way
 * back out of it — stays drawn either way; it is the one door that is not also
 * on the profile page. A folded door that is the current page is drawn anyway,
 * because the pill that says where you are must never be the thing hidden.
 * Nothing animates: the rows are there or not, the same rule as `Disclosure`.
 */
export function AccountCard({
  userId,
  username,
  isAdmin,
  inAdmin,
  pathname,
  back,
}: {
  userId: number | null
  username: string
  isAdmin: boolean
  inAdmin: boolean
  pathname: string
  /** Inside Administration: where the way out leads, and what to call it. */
  back: { to: string; label: string }
}) {
  const short = useMediaQuery(SHORT_VIEWPORT)
  const [open, setOpen] = useDisclosureState('shell.account-doors', userId)

  const initials = username.slice(0, 2).toUpperCase()
  const onDoor = pathname.startsWith(ACCESS_HOME) || pathname.startsWith(CREDENTIALS_HOME)
  // Only the personal doors fold, and only outside Administration — inside it
  // the card holds nothing but the way back.
  const foldable = short && !inAdmin
  const doorsShown = !foldable || open || onDoor

  return (
    <div className="m-3 mt-0 flex shrink-0 flex-col gap-1 rounded-card border border-accent-line bg-linear-to-b from-accent-soft to-rail p-2">
      <div className="flex items-center gap-1">
        <NavLink
          to={PROFILE_HOME}
          title="Your profile"
          className="nav-pill group flex min-w-0 flex-1 items-center gap-2.5 rounded-control px-1.5 pt-1 pb-1.5"
        >
          <span className="grid size-9 shrink-0 place-items-center rounded-full border border-accent-line bg-rail font-data text-[12px] font-semibold text-rail-fg group-hover:bg-transparent group-focus-visible:bg-transparent group-aria-[current=page]:bg-transparent">
            {initials}
          </span>
          <span className="min-w-0 flex-1 leading-tight">
            <span className="block truncate text-[13.5px] font-semibold text-rail-fg">{username}</span>
            <span className="block truncate text-[12px] text-rail-faint">
              {isAdmin ? 'Administrator' : 'Developer'}
            </span>
          </span>
        </NavLink>
        {foldable && !onDoor ? (
          <button
            type="button"
            onClick={() => setOpen(!open)}
            aria-expanded={open}
            aria-controls="account-doors"
            title={open ? 'Hide My access and My credentials' : 'Show My access and My credentials'}
            className="grid size-8 shrink-0 cursor-pointer place-items-center rounded-control text-rail-muted transition-colors duration-300 hover:bg-rail-raised hover:text-rail-fg motion-reduce:transition-none"
          >
            <ChevronDown aria-hidden="true" className={`size-4 ${open ? 'rotate-180' : ''}`} />
            <span className="sr-only">{open ? 'Hide your doors' : 'Show your doors'}</span>
          </button>
        ) : null}
      </div>

      {inAdmin ? (
        <Link to={back.to} className={ROW}>
          <ArrowLeft aria-hidden="true" className="size-4 shrink-0" />
          <span className="min-w-0 flex-1 truncate">{back.label}</span>
        </Link>
      ) : (
        <>
          {doorsShown ? (
            <div id="account-doors" className="flex flex-col gap-1">
              <NavLink to={ACCESS_HOME} className={ROW}>
                <Timer aria-hidden="true" className="size-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate">My access</span>
              </NavLink>
              {/* Beside it rather than under Administration: these are the
                  credentials this person holds — the kubeconfigs and the
                  password — and neither is somebody else's to manage. */}
              <NavLink to={CREDENTIALS_HOME} className={ROW}>
                <FileKey aria-hidden="true" className="size-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate">My credentials</span>
              </NavLink>
            </div>
          ) : null}
          {/* The one door. Absent, not disabled, for a non-admin: every row
              behind it would refuse, and a door that never opens is worse than
              no door. */}
          {isAdmin ? (
            <NavLink to={ADMIN_HOME} className={ROW}>
              <SlidersHorizontal aria-hidden="true" className="size-4 shrink-0" />
              <span className="min-w-0 flex-1 truncate">Administration</span>
              <ChevronRight aria-hidden="true" className="size-3.5 shrink-0" />
            </NavLink>
          ) : null}
        </>
      )}
    </div>
  )
}
