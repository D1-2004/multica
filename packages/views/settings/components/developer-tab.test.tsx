import {fireEvent,screen} from "@testing-library/react";
import {describe,it,expect,vi,beforeEach} from "vitest";
import {renderWithI18n} from "../../test/i18n";
const state=vi.hoisted(()=>({developer:true,save:vi.fn(),discover:vi.fn()}));
vi.mock("@multica/core/global-models",()=>({
 useTestProviderModel:()=>({mutateAsync:vi.fn()}),
 useDeveloperCapabilities:()=>({data:{developer:state.developer}}),
 useGlobalModels:()=>({data:{revision:1,providers:[{id:"mass",name:"Diamond",baseUrl:"https://default.example/v1",models:["qwen"],enabled:true,builtin:true,hasKey:true,apiKey:""}],agentModels:[{provider:"mass",model:"qwen"}],defaultModel:{provider:"mass",model:"qwen"},coordinator:[{provider:"mass",model:"qwen"}],diamondFallback:true}}),
 useSaveGlobalModels:()=>({mutateAsync:state.save}),useDiscoverProviderModels:()=>({mutateAsync:state.discover}),useRestoreGlobalModels:()=>({mutate:vi.fn()})
}));
vi.mock("../../runtimes/components/stable-fc-e2b-runtime-overview-page",()=>({StableFCE2BRuntimeOverviewPage:()=> <div data-testid="runtime-release"/>}));
import {DeveloperTab} from "./developer-tab";
beforeEach(()=>{state.developer=true;state.save.mockReset();state.discover.mockReset()});
describe("developer settings",()=>{
 it("hides global editing from non developers",()=>{state.developer=false;renderWithI18n(<DeveloperTab/>);expect(screen.getByText("Developer access required")).toBeInTheDocument();expect(screen.queryByText("Add provider")).toBeNull()});
 it("keeps Diamond readonly and hosts runtime release controls",()=>{renderWithI18n(<DeveloperTab/>);expect(screen.getByDisplayValue("https://default.example/v1")).toBeDisabled();expect(screen.queryByLabelText("API Key")).toBeNull();fireEvent.click(screen.getByRole("button",{name:"Runtime releases"}));expect(screen.getByTestId("runtime-release")).toBeInTheDocument()});
 it("preserves newlines while manually editing a provider catalog",()=>{renderWithI18n(<DeveloperTab/>);fireEvent.click(screen.getByRole("button",{name:"Add provider"}));const catalogs=screen.getAllByLabelText("Model catalog (one model ID per line)");const input=catalogs[catalogs.length-1]!;fireEvent.change(input,{target:{value:"model-one\n"}});expect(input).toHaveValue("model-one\n")});
});
