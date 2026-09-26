export type PreferenceLevel =
  | "WANT"
  | "OKAY"
  | "NO"
  | "NEVER";

export interface RoomOption {
  id: string;
  text: string;
}

export interface ParticipantStatus {
  id: string;
  name: string;
  hasVoted: boolean;
}

export interface OptionResult {
  optionId: string;
  text: string;
  want: number;
  okay: number;
  no: number;
  never: number;
  score: number;
  vetoed: boolean;
}

export interface ResultDto {
  matchType: string;
  options: OptionResult[];
  topOptionId?: string;
}

export interface StatusResponse {
  code: string;
  question: string;
  category: string;
  options: RoomOption[];
  participants: ParticipantStatus[];
  totalParticipants: number;
  votedCount: number;
  allVoted: boolean;
  result?: ResultDto | null;
}

export interface JoinResponse {
  participantId: string;
  code: string;
  question: string;
  category: string;
  options: RoomOption[];
}